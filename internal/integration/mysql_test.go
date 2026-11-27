//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/client"
	gomysql "github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/integration/it"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/mysql/scripted"
)

const (
	// Ingress counts a call under its own path when no migration state lists a pattern.
	merchantOneOrdersRoute = "GET /merchants/1/orders"

	// Merchant 1 has three orders, and order 8 has no items.
	merchantOneOrders = `{"orders":[` +
		`{"id":7,"label":"A","items":[{"id":1,"sku":"apple","quantity":2},{"id":2,"quantity":1}]},` +
		`{"id":8,"label":"B"},` +
		`{"id":9,"label":"C","items":[{"id":3,"sku":"pear","quantity":5}]}]}`

	ordersSchema = `
		declare module "orders" {
		  export interface MerchantOrders {
		    orders?: Order[];
		  }

		  export interface Order {
		    id: number;
		    label: string;
		    items?: Item[];
		  }

		  export interface Item {
		    id: number;
		    sku?: string;
		    quantity: number;
		  }
		}
	`

	selectOrdersDigest = "SELECT `id` , `label` FROM `orders` WHERE `merchant_id` = ? ORDER BY `id`"
	selectItemsDigest  = "SELECT `id` , `sku` , `quantity` FROM `items` WHERE `order_id` = ? ORDER BY `id`"
	selectJoinDigest   = "SELECT `o` . `id` , `o` . `label` , `i` . `id` AS `item` , `i` . `sku` , `i` . `quantity` " +
		"FROM `orders` `o` LEFT JOIN `items` `i` ON `i` . `order_id` = `o` . `id` WHERE `o` . `merchant_id` = ? ORDER BY `o` . `id` , `i` . `id`"

	// Merchant 1's orders 7 and 9 have items; order 8 has none, so it has no totals.
	merchantOneTotalsRoute = "GET /merchants/1/totals"
	merchantOneTotals      = `{"totals":[{"order":7,"lines":2,"quantity":3},{"order":9,"lines":1,"quantity":5}]}`
	totalsSchema           = `
		declare module "totals" {
		  export interface MerchantTotals {
		    totals?: OrderTotal[];
		  }

		  export interface OrderTotal {
		    order: number;
		    lines: number;
		    quantity: number;
		  }
		}
	`

	selectQuantitiesDigest = "SELECT `i` . `order_id` , `i` . `quantity` FROM `items` `i` JOIN `orders` `o` ON `o` . `id` = `i` . `order_id` WHERE `o` . `merchant_id` = ?"
)

func TestMySQLIdenticalStatementsReplayTheReferenceRows(t *testing.T) {
	m := startOrdersMirror(t)

	m.Ingress.Get("/merchants/1/orders").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneOrders)
	// Ingress counts the call once both services answered alike, so the candidate
	// read the same orders from the reference's replies.
	m.ExpectCalls(it.Calls{merchantOneOrdersRoute: 1})
	// Only the reference's statements reach the database, as the server digests them.
	m.MySQL().ExpectStatements(map[string]uint64{selectOrdersDigest: 1, selectItemsDigest: 3})
}

func TestMySQLCandidateStatementTheReferenceNeverRanQuarantines(t *testing.T) {
	m := startOrdersMirror(t)

	// No reference statement read order 99's items, so the candidate gets an error, not rows.
	_, err := selectItemsOf(t.Context(), m.MySQL().Candidate, it.InTestRequest, 99)
	assert.Contains(t, err.Error(), "SPECTRE: no matching reference reply")
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantineUnmatched, "SELECT", "no matching reference request within 500ms"))
	m.MySQL().ExpectStatements(map[string]uint64{})
}

func TestMySQLReferenceStatementTheCandidateNeverRanQuarantines(t *testing.T) {
	m := startOrdersMirror(t)

	// The reference reads order 7's items, and the candidate never asks for them.
	items, err := selectItemsOf(t.Context(), m.MySQL().Reference, it.InTestRequest, 7)
	assert.NoError(t, err)
	assert.Equal(t, []item{{ID: 1, SKU: Some("apple"), Quantity: 2}, {ID: 2, Quantity: 1}}, items)
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantineUnmatched, "SELECT", "no matching candidate request within 500ms"))
}

func TestMySQLCandidateStatementWithoutARequestIDQuarantines(t *testing.T) {
	m := startOrdersMirror(t)

	_, err := m.MySQL().Candidate.ExecContext(t.Context(), "SELECT sku FROM items WHERE id = 3")
	assert.Contains(t, err.Error(), "SPECTRE: no matching reference reply")
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantinePairing, "SELECT", "candidate request carries no SPECTRE request ID"))
}

func TestMySQLCandidateStatementWithAMalformedRequestIDQuarantines(t *testing.T) {
	m := startOrdersMirror(t)

	_, err := m.MySQL().Candidate.ExecContext(t.Context(), "SELECT sku FROM items WHERE id = 3 /* srid_nonsense */")
	assert.Contains(t, err.Error(), "SPECTRE: request ID is invalid")
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantinePairing, "SELECT", "statement carries a malformed SPECTRE request ID"))
}

// No candidate statement can pair with a reference statement that names no request, so it quarantines.
func TestMySQLReferenceStatementWithoutARequestIDQuarantines(t *testing.T) {
	m := startOrdersMirror(t)

	items, err := selectItemsOf(t.Context(), m.MySQL().Reference, func(statement string) string { return statement }, 7)
	assert.NoError(t, err)
	assert.Equal(t, []item{{ID: 1, SKU: Some("apple"), Quantity: 2}, {ID: 2, Quantity: 1}}, items)
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantinePairing, "SELECT", "reference statement carries no SPECTRE request ID"))
}

// SET statements only change session state, so neither side's are paired, and they need no request ID.
func TestMySQLSetStatementsAreNotPaired(t *testing.T) {
	m := startOrdersMirror(t)

	_, err := m.MySQL().Reference.ExecContext(t.Context(), "SET time_zone = '+00:00'")
	assert.NoError(t, err)
	_, err = m.MySQL().Candidate.ExecContext(t.Context(), "SET @@SESSION.sql_mode = CONCAT(@@sql_mode, ',STRICT_ALL_TABLES'), @mark = 1")
	assert.NoError(t, err)

	// The candidate is not quarantined, so ingress still compares a call.
	m.Ingress.Get("/merchants/1/orders").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneOrders)
	m.ExpectCalls(it.Calls{merchantOneOrdersRoute: 1})
}

// The candidate reads another order's items than the reference read for the same request,
// so egress reports how the two statements differ, without their values.
func TestMySQLCandidateStatementWithOtherValuesDiverges(t *testing.T) {
	m := startOrdersMirror(t)

	_, err := selectItemsOf(t.Context(), m.MySQL().Reference, it.InTestRequest, 7)
	assert.NoError(t, err)
	_, err = selectItemsOf(t.Context(), m.MySQL().Candidate, it.InTestRequest, 9)
	assert.Contains(t, err.Error(), "SPECTRE: no matching reference reply")
	m.ExpectQuarantine(model.QuarantineReason{
		Kind:        model.QuarantineDivergent,
		Traffic:     Some(model.Traffic{Direction: model.Egress, Protocol: scripted.Protocol, Route: "SELECT"}),
		Differences: []model.Difference{{Path: "$.key.statement", Change: model.ChangeChanged, Count: 1}},
	})
}

func TestMySQLReferenceRepliesBeyondTheSavedSizeQuarantine(t *testing.T) {
	m := startOrdersMirror(t, it.MySQLSavedRepliesBytes(64))

	// The reference is answered in full, although the proxy has no room to save its replies.
	m.Ingress.Get("/merchants/1/orders").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneOrders)
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantineCapacity, "SELECT", "saved reference replies exceed the MySQL saved replies size"))
}

func TestMySQLCandidateWritesReplayTheReferenceReplies(t *testing.T) {
	m := startOrdersMirror(t)

	reference, err := addItem(t.Context(), m.MySQL().Reference)
	assert.NoError(t, err)
	// The same write would break the primary key if it reached the database.
	candidate, err := addItem(t.Context(), m.MySQL().Candidate)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), reference)
	assert.Equal(t, reference, candidate)
	m.MySQL().ExpectStatements(map[string]uint64{
		"START TRANSACTION": 1,
		"INSERT INTO `items` ( `id` , `order_id` , `sku` , `quantity` ) VALUES (...)": 1,
		"COMMIT": 1,
	})
}

// Two transactions each add an order for merchant 3, then read only their own uncommitted
// order. The candidate's run in the other order, and each still reads its own.
func TestMySQLCandidateTransactionsReadTheirOwnReferenceTransaction(t *testing.T) {
	const selectOrders = "SELECT id, label FROM orders WHERE merchant_id = 3 ORDER BY id"
	m := startOrdersMirror(t, it.Reference(idleService), it.Candidate(idleService))
	insert := func(transaction *sql.Conn, id int, label string) {
		_, err := transaction.ExecContext(t.Context(), it.InTestRequest("INSERT INTO orders (id, merchant_id, label) VALUES (?, 3, ?)"), id, label)
		assert.NoError(t, err)
	}

	first, second := begin(t, m.MySQL().Reference), begin(t, m.MySQL().Reference)
	insert(first, 100, "first")
	insert(second, 200, "second")
	_, firstOrders := queried(t, first, selectOrders)
	_, secondOrders := queried(t, second, selectOrders)
	commit(t, first)
	commit(t, second)
	assert.Equal(t, [][]Option[string]{{Some("100"), Some("first")}}, firstOrders)
	assert.Equal(t, [][]Option[string]{{Some("200"), Some("second")}}, secondOrders)

	// The second request's transaction reads first, as the first's did on the reference.
	candidateSecond, candidateFirst := begin(t, m.MySQL().Candidate), begin(t, m.MySQL().Candidate)
	insert(candidateSecond, 200, "second")
	insert(candidateFirst, 100, "first")
	_, candidateSecondOrders := queried(t, candidateSecond, selectOrders)
	_, candidateFirstOrders := queried(t, candidateFirst, selectOrders)
	commit(t, candidateSecond)
	commit(t, candidateFirst)
	assert.Equal(t, secondOrders, candidateSecondOrders)
	assert.Equal(t, firstOrders, candidateFirstOrders)
	m.ExpectCalls(it.Calls{})
}

// The reference reads merchant 1's orders on their own, then in a transaction; the
// candidate runs its transaction first, which still pairs with the reference's transaction.
func TestMySQLCandidateTransactionsPairOnlyWithReferenceTransactions(t *testing.T) {
	const selectOrders = "SELECT id, label FROM orders WHERE merchant_id = 1 ORDER BY id"
	m := startOrdersMirror(t, it.Reference(idleService), it.Candidate(idleService))
	inTransaction := func(database *sql.DB) {
		transaction := begin(t, database)
		queried(t, transaction, selectOrders)
		commit(t, transaction)
	}

	queried(t, m.MySQL().Reference, selectOrders)
	inTransaction(m.MySQL().Reference)
	inTransaction(m.MySQL().Candidate)
	queried(t, m.MySQL().Candidate, selectOrders)

	m.ExpectCalls(it.Calls{})
}

func TestMySQLCandidateReplaysTheReferenceDatabaseError(t *testing.T) {
	m := startOrdersMirror(t)

	for _, database := range []*sql.DB{m.MySQL().Reference, m.MySQL().Candidate} {
		_, err := database.ExecContext(t.Context(), it.InTestRequest("SELECT id FROM missing"))
		assert.Contains(t, err.Error(), "Error 1146 (42S02): Table '"+m.MySQL().Name+".missing' doesn't exist")
	}
	m.MySQL().ExpectStatements(map[string]uint64{"SELECT `id` FROM `missing`": 1})
}

func TestMySQLStatementsPairWhateverTheirCommentsAndSpacing(t *testing.T) {
	m := startOrdersMirror(t)

	sku := ""
	assert.NoError(t, m.MySQL().Reference.QueryRowContext(t.Context(), it.InTestRequest("SELECT sku FROM items WHERE id = 3 /* reference */")).Scan(&sku))
	assert.Equal(t, "pear", sku)
	sku = ""
	assert.NoError(t, m.MySQL().Candidate.QueryRowContext(t.Context(), it.InTestRequest("select sku\n  from items where id=3 -- candidate")).Scan(&sku))
	assert.Equal(t, "pear", sku)
	m.MySQL().ExpectStatements(map[string]uint64{"SELECT `sku` FROM `items` WHERE `id` = ?": 1})
}

// unusedJoinScript declares that a JOIN replaces the orders service's statements, though
// neither service runs the JOIN.
const unusedJoinScript = `
	import { egress } from "spectre";

	egress.mysql
	  .statement(
	    "SELECT o.id, o.label, i.id AS item, i.sku, i.quantity FROM orders o " +
	      "LEFT JOIN items i ON i.order_id = o.id WHERE o.merchant_id = ? ORDER BY o.id, i.id",
	  )
	  .replaces("SELECT id, label FROM orders WHERE merchant_id = ? ORDER BY id", "SELECT id, sku, quantity FROM items WHERE order_id = ? ORDER BY id")
	  .using(() => []);
`

// Scripts declare a replacement, but the candidate is unchanged: the replacement's own
// statements, and statements it does not name, writes included, still pair exactly.
func TestMySQLStatementsPairExactlyAlongsideReplacements(t *testing.T) {
	m := startOrdersMirror(t, it.Scripts(unusedJoinScript))

	m.Ingress.Get("/merchants/1/orders").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneOrders)
	m.ExpectCalls(it.Calls{merchantOneOrdersRoute: 1})
	sku := ""
	assert.NoError(t, m.MySQL().Reference.QueryRowContext(t.Context(), it.InTestRequest("SELECT sku FROM items WHERE id = 3 /* reference */")).Scan(&sku))
	assert.Equal(t, "pear", sku)
	sku = ""
	assert.NoError(t, m.MySQL().Candidate.QueryRowContext(t.Context(), it.InTestRequest("select sku\n  from items where id=3 -- candidate")).Scan(&sku))
	assert.Equal(t, "pear", sku)
	reference, err := addItem(t.Context(), m.MySQL().Reference)
	assert.NoError(t, err)
	candidate, err := addItem(t.Context(), m.MySQL().Candidate)
	assert.NoError(t, err)
	assert.Equal(t, reference, candidate)
	// Each statement reached the database once, from the reference.
	m.MySQL().ExpectStatements(map[string]uint64{
		selectOrdersDigest: 1, selectItemsDigest: 3,
		"SELECT `sku` FROM `items` WHERE `id` = ?": 1,
		"START TRANSACTION":                        1,
		"INSERT INTO `items` ( `id` , `order_id` , `sku` , `quantity` ) VALUES (...)": 1,
		"COMMIT": 1,
	})
}

// Scripts declare a replacement, and the candidate reads the items of the order the
// reference read, then of one it never read: a replacement's statement pairs by its values.
func TestMySQLReplacementStatementWithOtherValuesQuarantines(t *testing.T) {
	m := startOrdersMirror(t, it.Scripts(unusedJoinScript))

	reference, err := selectItemsOf(t.Context(), m.MySQL().Reference, it.InTestRequest, 7)
	assert.NoError(t, err)
	candidate, err := selectItemsOf(t.Context(), m.MySQL().Candidate, it.InTestRequest, 7)
	assert.NoError(t, err)
	assert.Equal(t, reference, candidate)
	_, err = selectItemsOf(t.Context(), m.MySQL().Candidate, it.InTestRequest, 9)
	assert.Contains(t, err.Error(), "SPECTRE: no matching reference reply")
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantineUnmatched, "SELECT", "no matching reference request within 500ms"))
	m.MySQL().ExpectStatements(map[string]uint64{selectItemsDigest: 1})
}

// The reference reads a merchant's orders, then each order's items (N+1); the rewritten
// candidate reads the same data with one JOIN.
func TestMySQLCandidateJoinReplacesTheReferenceStatements(t *testing.T) {
	m := startOrdersMirror(t, it.Candidate(joinedOrdersService()), it.Scripts(`
		import { egress } from "spectre";

		interface Order { id: string; label: string }
		interface Item { id: string; sku: string | null; quantity: string }

		egress.mysql
		  .statement(
		    "SELECT o.id, o.label, i.id AS item, i.sku, i.quantity FROM orders o " +
		      "LEFT JOIN items i ON i.order_id = o.id WHERE o.merchant_id = ? ORDER BY o.id, i.id",
		  )
		  .replaces<[Order, Item]>(
		    "SELECT id, label FROM orders WHERE merchant_id = ? ORDER BY id",
		    "SELECT id, sku, quantity FROM items WHERE order_id = ? ORDER BY id",
		  )
		  .using(([merchant], [orders, items]) => {
		    const rows = [];
		    for (const order of orders(merchant)) {
		      const found = items(order.id);
		      if (found.length === 0) rows.push({ id: order.id, label: order.label, item: null, sku: null, quantity: null });
		      for (const item of found) rows.push({ id: order.id, label: order.label, item: item.id, sku: item.sku, quantity: item.quantity });
		    }
		    return rows;
		  });
	`))

	m.Ingress.Get("/merchants/1/orders").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneOrders)
	// The candidate's JOIN was answered from the reference's orders and items statements,
	// and claimed every one of them, so ingress compared alike responses.
	m.ExpectCalls(it.Calls{merchantOneOrdersRoute: 1})
	m.MySQL().ExpectStatements(map[string]uint64{selectOrdersDigest: 1, selectItemsDigest: 3})
}

// The reference reads a merchant's orders and items with one JOIN; the rewritten candidate
// reads the orders, then each order's items (N+1).
func TestMySQLCandidateStatementsReplaceTheReferenceJoin(t *testing.T) {
	m := startOrdersMirror(t, it.Reference(joinedOrdersService()), it.Scripts(`
		import { egress } from "spectre";

		interface Order { id: string; label: string }
		interface Item { id: string; sku: string | null; quantity: string }
		interface OrderItem { id: string; label: string; item: string | null; sku: string | null; quantity: string | null }

		egress.mysql
		  .statements<[Order, Item]>(
		    "SELECT id, label FROM orders WHERE merchant_id = ? ORDER BY id",
		    "SELECT id, sku, quantity FROM items WHERE order_id = ? ORDER BY id",
		  )
		  .replaces<OrderItem>(
		    "SELECT o.id, o.label, i.id AS item, i.sku, i.quantity FROM orders o " +
		      "LEFT JOIN items i ON i.order_id = o.id WHERE o.merchant_id = ? ORDER BY o.id, i.id",
		  )
		  .using(([merchant], rows, [orders, items]) => {
		    const labels = new Map<string, string>();
		    const found = new Map<string, Item[]>();
		    for (const row of rows) {
		      labels.set(row.id, row.label);
		      const list = found.get(row.id) ?? [];
		      if (row.item !== null) list.push({ id: row.item, sku: row.sku, quantity: row.quantity! });
		      found.set(row.id, list);
		    }
		    orders(merchant, [...labels].map(([id, label]) => ({ id, label })));
		    for (const [order, list] of found) items(order, list);
		  });
	`))

	m.Ingress.Get("/merchants/1/orders").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneOrders)
	// Each of the candidate's statements claimed a reply split from the reference's JOIN.
	m.ExpectCalls(it.Calls{merchantOneOrdersRoute: 1})
	m.MySQL().ExpectStatements(map[string]uint64{selectJoinDigest: 1})
}

// The reference reads every item of a merchant and totals them by order in code; the
// rewritten candidate has the database total them with GROUP BY.
func TestMySQLCandidateGroupByReplacesTheReferenceRows(t *testing.T) {
	m := startOrdersMirror(t,
		it.Reference(totalsService(totalsGroupedInCode)), it.Candidate(totalsService(totalsGroupedInSQL)),
		it.Schema(totalsSchema), it.Scripts(`
			import { egress, ingress } from "spectre";
			import type { MerchantTotals } from "totals";

			ingress.http("GET /merchants/{merchant}/totals").match<MerchantTotals>();

			interface Quantity { order_id: string; quantity: string }
			interface Total { order_id: string; line_count: string; total: string }

			egress.mysql
			  .statement<Total>(
			    "SELECT i.order_id, COUNT(*) AS line_count, SUM(i.quantity) AS total FROM items i " +
			      "JOIN orders o ON o.id = i.order_id WHERE o.merchant_id = ? GROUP BY i.order_id ORDER BY i.order_id",
			  )
			  .replaces<[Quantity]>("SELECT i.order_id, i.quantity FROM items i JOIN orders o ON o.id = i.order_id WHERE o.merchant_id = ?")
			  .using(([merchant], [quantitiesOf]) => {
			    const groups = new Map<string, { lines: number; total: number }>();
			    for (const row of quantitiesOf(merchant)) {
			      const group = groups.get(row.order_id) ?? { lines: 0, total: 0 };
			      group.lines += 1;
			      group.total += Number(row.quantity);
			      groups.set(row.order_id, group);
			    }
			    return [...groups]
			      .sort(([left], [right]) => Number(left) - Number(right))
			      .map(([order, { lines, total }]) => ({ order_id: order, line_count: String(lines), total: String(total) }));
			  });
		`))

	m.Ingress.Get("/merchants/1/totals").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneTotals)
	// The candidate's GROUP BY was answered by grouping the rows the reference grouped itself.
	m.ExpectCalls(it.Calls{merchantOneTotalsRoute: 1})
	m.MySQL().ExpectStatements(map[string]uint64{selectQuantitiesDigest: 1})
}

// The candidate's GROUP BY reads its aggregates with a driver that converts values by
// column type, so its reply must look exactly like the database's own.
func TestMySQLCandidateGroupByGetsTheReplyTheDatabaseWouldSend(t *testing.T) {
	const (
		items   = "SELECT i.order_id, i.id, i.sku, i.quantity FROM items i JOIN orders o ON o.id = i.order_id WHERE o.merchant_id = ?"
		grouped = "SELECT i.order_id, COUNT(*) AS line_count, SUM(i.quantity) AS total, AVG(i.quantity) AS average, " +
			"MIN(i.sku) AS first_sku, MAX(i.id) AS last_item FROM items i JOIN orders o ON o.id = i.order_id " +
			"WHERE o.merchant_id = ? GROUP BY i.order_id ORDER BY i.order_id"
	)
	m := startOrdersMirror(t, it.Scripts(`
		import { egress, type Row } from "spectre";

		egress.mysql
		  .statement("`+grouped+`")
		  .replaces("`+items+`")
		  .using(([merchant], [itemsOf]) => {
		    const groups = new Map<string, Row[]>();
		    for (const row of itemsOf(merchant)) groups.set(row.order_id!, [...(groups.get(row.order_id!) ?? []), row]);
		    return [...groups]
		      .sort(([left], [right]) => Number(left) - Number(right))
		      .map(([order, rows]) => {
		        const total = rows.reduce((sum, row) => sum + Number(row.quantity), 0);
		        const skus = rows.flatMap((row) => (row.sku === null ? [] : [row.sku!])).sort();
		        return {
		          order_id: order,
		          line_count: String(rows.length),
		          total: String(total),
		          average: (total / rows.length).toFixed(4),
		          first_sku: skus[0] ?? null,
		          last_item: String(Math.max(...rows.map((row) => Number(row.id)))),
		        };
		      });
		  });
	`))

	queried(t, m.MySQL().Reference, items, 1)
	candidateColumns, candidateRows := queried(t, m.MySQL().Candidate, grouped, 1)
	databaseColumns, databaseRows := queried(t, databaseOf(t, m), grouped, 1)

	assert.Equal(t, [][]Option[string]{
		{Some("7"), Some("2"), Some("3"), Some("1.5000"), Some("apple"), Some("2")},
		{Some("9"), Some("1"), Some("5"), Some("5.0000"), Some("pear"), Some("3")},
	}, databaseRows)
	assert.Equal(t, databaseRows, candidateRows)
	assert.Equal(t, databaseColumns, candidateColumns)
	// The GROUP BY reached the database once, from the test itself, not from the candidate.
	m.MySQL().ExpectStatements(map[string]uint64{
		"SELECT `i` . `order_id` , `i` . `id` , `i` . `sku` , `i` . `quantity` FROM `items` `i` JOIN `orders` `o` ON `o` . `id` = `i` . `order_id` WHERE `o` . `merchant_id` = ?": 1,
		"SELECT `i` . `order_id` , COUNT ( * ) AS `line_count` , SUM ( `i` . `quantity` ) AS `total` , AVG ( `i` . `quantity` ) AS `average` , " +
			"MIN ( `i` . `sku` ) AS `first_sku` , MAX ( `i` . `id` ) AS `last_item` FROM `items` `i` JOIN `orders` `o` ON `o` . `id` = `i` . `order_id` " +
			"WHERE `o` . `merchant_id` = ? GROUP BY `i` . `order_id` ORDER BY `i` . `order_id`": 1,
	})
}

// The reference reads a department's employees, its name, then each manager once; the
// rewritten candidate joins employees to their managers and department in one statement.
func TestMySQLCandidateSelfJoinGetsTheReplyTheDatabaseWouldSend(t *testing.T) {
	const (
		employees  = "SELECT id, name, manager_id FROM employees WHERE department_id = ? ORDER BY id"
		department = "SELECT name FROM departments WHERE id = ?"
		employee   = "SELECT id, name FROM employees WHERE id = ?"
		// The self join finds each employee's manager, if they have one, and their department.
		staff = "SELECT e.id, e.name, e.manager_id, m.name AS manager, d.name AS department FROM employees e " +
			"LEFT JOIN employees m ON m.id = e.manager_id JOIN departments d ON d.id = e.department_id WHERE e.department_id = ? ORDER BY e.id"
	)
	m := startStaffMirror(t, `
		import { egress } from "spectre";

		interface Employee { id: string; name: string; manager_id: string | null }
		interface Department { name: string }
		interface Staff { id: string; name: string; manager_id: string | null; manager: string | null; department: string }

		egress.mysql
		  .statement<Staff>("`+staff+`")
		  .replaces<[Employee, Department, Pick<Employee, "id" | "name">]>("`+employees+`", "`+department+`", "`+employee+`")
		  .using(([department], [inDepartment, departmentNamed, employee]) => {
		    const employees = inDepartment(department);
		    const [named] = departmentNamed(department);
		    const managers = new Map<string, string | null>();
		    for (const row of employees) {
		      if (row.manager_id !== null && !managers.has(row.manager_id)) {
		        managers.set(row.manager_id, employee(row.manager_id)[0]?.name ?? null);
		      }
		    }
		    if (!named) return [];
		    return employees.map((row) => ({
		      ...row,
		      manager: row.manager_id === null ? null : managers.get(row.manager_id)!,
		      department: named.name,
		    }));
		  });
	`)

	queried(t, m.MySQL().Reference, employees, 1)
	queried(t, m.MySQL().Reference, department, 1)
	queried(t, m.MySQL().Reference, employee, 1)
	queried(t, m.MySQL().Reference, employee, 2)
	candidateColumns, candidateRows := queried(t, m.MySQL().Candidate, staff, 1)
	databaseColumns, databaseRows := queried(t, databaseOf(t, m), staff, 1)

	assert.Equal(t, [][]Option[string]{
		{Some("1"), Some("Ada"), None[string](), None[string](), Some("Engineering")},
		{Some("2"), Some("Grace"), Some("1"), Some("Ada"), Some("Engineering")},
		{Some("3"), Some("Linus"), Some("2"), Some("Grace"), Some("Engineering")},
	}, databaseRows)
	assert.Equal(t, databaseRows, candidateRows)
	// MySQL sends the manager's name, NOT NULL in its table, as nullable from the LEFT JOIN.
	assert.Equal(t, databaseColumns, candidateColumns)
	assert.True(t, candidateColumns[3].Nullable)
	// The JOIN reached the database once, from the test itself, not from the candidate.
	m.MySQL().ExpectStatements(map[string]uint64{
		"SELECT `id` , NAME , `manager_id` FROM `employees` WHERE `department_id` = ? ORDER BY `id`": 1,
		"SELECT NAME FROM `departments` WHERE `id` = ?":                                              1,
		"SELECT `id` , NAME FROM `employees` WHERE `id` = ?":                                         2,
		"SELECT `e` . `id` , `e` . `name` , `e` . `manager_id` , `m` . `name` AS `manager` , `d` . `name` AS `department` FROM `employees` `e` " +
			"LEFT JOIN `employees` `m` ON `m` . `id` = `e` . `manager_id` JOIN `departments` `d` ON `d` . `id` = `e` . `department_id` " +
			"WHERE `e` . `department_id` = ? ORDER BY `e` . `id`": 1,
	})
}

// The reference reads a department's staff with one self join; the rewritten candidate reads
// its employees, its name, then each manager once, from the database's point of view.
func TestMySQLCandidateStatementsGetTheRepliesTheDatabaseWouldSendFromASelfJoin(t *testing.T) {
	const (
		employees  = "SELECT id, name, manager_id FROM employees WHERE department_id = ? ORDER BY id"
		department = "SELECT name FROM departments WHERE id = ?"
		employee   = "SELECT id, name FROM employees WHERE id = ?"
		// The self join finds each employee's manager, if they have one, and their department.
		staff = "SELECT e.id, e.name, e.manager_id, m.name AS manager, d.name AS department FROM employees e " +
			"LEFT JOIN employees m ON m.id = e.manager_id JOIN departments d ON d.id = e.department_id WHERE e.department_id = ? ORDER BY e.id"
	)
	m := startStaffMirror(t, `
		import { egress } from "spectre";

		interface Employee { id: string; name: string; manager_id: string | null }
		interface Department { name: string }
		interface Staff { id: string; name: string; manager_id: string | null; manager: string | null; department: string }

		egress.mysql
		  .statements<[Employee, Department, Pick<Employee, "id" | "name">]>("`+employees+`", "`+department+`", "`+employee+`")
		  .replaces<Staff>("`+staff+`")
		  .using(([department], rows, [inDepartment, departmentNamed, employee]) => {
		    inDepartment(department, rows.map(({ id, name, manager_id }) => ({ id, name, manager_id })));
		    if (rows.length > 0) departmentNamed(department, [{ name: rows[0]!.department }]);
		    const managers = new Map<string, string | null>();
		    for (const row of rows) if (row.manager_id !== null) managers.set(row.manager_id, row.manager);
		    // name is NOT NULL, so a NULL manager name means the manager row doesn't exist.
		    for (const [id, name] of managers) employee(id, name === null ? [] : [{ id, name }]);
		  });
	`)

	queried(t, m.MySQL().Reference, staff, 1)
	database := databaseOf(t, m)
	for _, statement := range []struct {
		sql   string
		value int
	}{
		{employees, 1}, {department, 1}, {employee, 1}, {employee, 2},
	} {
		candidateColumns, candidateRows := queried(t, m.MySQL().Candidate, statement.sql, statement.value)
		databaseColumns, databaseRows := queried(t, database, statement.sql, statement.value)
		assert.NotZero(t, databaseRows)
		assert.Equal(t, databaseRows, candidateRows, statement.sql)
		assert.Equal(t, databaseColumns, candidateColumns, statement.sql)
	}
	// Only the test itself ran the candidate's statements on the database.
	m.MySQL().ExpectStatements(map[string]uint64{
		"SELECT `id` , NAME , `manager_id` FROM `employees` WHERE `department_id` = ? ORDER BY `id`": 1,
		"SELECT NAME FROM `departments` WHERE `id` = ?":                                              1,
		"SELECT `id` , NAME FROM `employees` WHERE `id` = ?":                                         2,
		"SELECT `e` . `id` , `e` . `name` , `e` . `manager_id` , `m` . `name` AS `manager` , `d` . `name` AS `department` FROM `employees` `e` " +
			"LEFT JOIN `employees` `m` ON `m` . `id` = `e` . `manager_id` JOIN `departments` `d` ON `d` . `id` = `e` . `department_id` " +
			"WHERE `e` . `department_id` = ? ORDER BY `e` . `id`": 1,
	})
}

func TestMySQLCandidateUsesTheDatabaseItSelected(t *testing.T) {
	m := startOrdersMirror(t)
	reference := m.MySQL().ConnectReference()
	_, err := reference.Execute("USE information_schema")
	assert.NoError(t, err)
	selected, err := reference.Execute(it.InTestRequest("SELECT DATABASE()"))
	assert.NoError(t, err)
	database, err := selected.GetString(0, 0)
	assert.NoError(t, err)
	assert.Equal(t, "information_schema", database)

	// The candidate pairs with the reference only once it selected the same database.
	candidate, err := m.MySQL().Candidate.Conn(t.Context())
	assert.NoError(t, err)
	defer candidate.Close()
	_, err = candidate.ExecContext(t.Context(), "USE `information_schema`")
	assert.NoError(t, err)
	assert.NoError(t, candidate.QueryRowContext(t.Context(), it.InTestRequest("SELECT DATABASE()")).Scan(&database))
	assert.Equal(t, "information_schema", database)
}

func TestMySQLCandidateStatementInAnotherDatabaseIsNotPaired(t *testing.T) {
	m := startOrdersMirror(t)
	reference := m.MySQL().ConnectReference()
	_, err := reference.Execute("USE information_schema")
	assert.NoError(t, err)
	_, err = reference.Execute(it.InTestRequest("SELECT DATABASE()"))
	assert.NoError(t, err)

	// The candidate stays in the database it connected to.
	_, err = m.MySQL().Candidate.ExecContext(t.Context(), it.InTestRequest("SELECT DATABASE()"))
	assert.Contains(t, err.Error(), "SPECTRE: no matching reference reply")
}

func TestMySQLReferenceConnectsWithTheProxyCredentials(t *testing.T) {
	m := startOrdersMirror(t)

	// The proxy accepts credentials the database does not know, which never reach it.
	assert.NoError(t, m.MySQL().OpenReference().PingContext(t.Context()))
}

func TestMySQLReferenceGetsTheDatabaseRefusal(t *testing.T) {
	m := startOrdersMirror(t)

	m.MySQL().SetProxyPassword("wrong")
	err := m.MySQL().OpenReference().PingContext(t.Context())
	assert.Contains(t, err.Error(), "Error 1045 (28000): Access denied for user 'it_")
}

func TestMySQLProxyReadsTheRotatedPassword(t *testing.T) {
	m := startOrdersMirror(t)

	m.MySQL().RotatePassword("rotated")
	err := m.MySQL().OpenReference().PingContext(t.Context())
	assert.Contains(t, err.Error(), "Error 1045 (28000): Access denied for user 'it_")
	m.MySQL().SetProxyPassword("rotated")
	assert.NoError(t, m.MySQL().OpenReference().PingContext(t.Context()))
}

// The candidate cannot send prepared statements, so the reference's quarantine it once they reach the database.
func TestMySQLReferencePreparedStatementQuarantines(t *testing.T) {
	m := startOrdersMirror(t)

	prepared, err := m.MySQL().Reference.PrepareContext(t.Context(), it.InTestRequest("SELECT sku FROM items WHERE id = ?"))
	assert.NoError(t, err)
	defer prepared.Close()
	var sku string
	assert.NoError(t, prepared.QueryRowContext(t.Context(), 3).Scan(&sku))
	assert.Equal(t, "pear", sku)
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantineUnable, "command", "prepared statements are not supported"))
}

func TestMySQLReferencePingReachesTheDatabase(t *testing.T) {
	m := startOrdersMirror(t)
	conn := m.MySQL().ConnectReference()
	assert.NoError(t, conn.Ping())

	result, err := conn.Execute("SELECT CONNECTION_ID()")
	assert.NoError(t, err)
	thread, err := result.GetUint(0, 0)
	assert.NoError(t, err)
	_, err = m.MySQL().Server().ExecContext(t.Context(), fmt.Sprintf("KILL %d", thread))
	assert.NoError(t, err)
	assert.Error(t, conn.Ping())
}

func TestMySQLReferenceCommandsReachTheDatabase(t *testing.T) {
	m := startOrdersMirror(t)
	conn := m.MySQL().ConnectReference()

	_, err := conn.Execute("SET @mark = 1")
	assert.NoError(t, err)
	assert.Equal(t, gomysql.OK_HEADER, command(t, conn, gomysql.COM_RESET_CONNECTION))
	result, err := conn.Execute("SELECT @mark")
	assert.NoError(t, err)
	mark, err := result.GetString(0, 0)
	assert.NoError(t, err)
	assert.Equal(t, "", mark)
	assert.Equal(t, gomysql.EOF_HEADER, command(t, conn, gomysql.COM_SET_OPTION, 0, 0))
}

func TestMySQLReferenceKillsItsOwnQuery(t *testing.T) {
	m := startOrdersMirror(t)
	sleeping := m.MySQL().ConnectReference()
	done := make(chan *gomysql.Result, 1)
	go func() {
		result, _ := sleeping.Execute("SELECT SLEEP(60)")
		done <- result
	}()
	m.MySQL().AwaitRunning("SELECT SLEEP(60)")

	// The thread the proxy reported names the query's own database thread.
	_, err := m.MySQL().ConnectReference().Execute(fmt.Sprintf("KILL QUERY %d", sleeping.GetConnectionID()))
	assert.NoError(t, err)
	// A killed sleep ends early and returns 1.
	interrupted, err := (<-done).GetInt(0, 0)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), interrupted)
}

func TestMySQLReferenceCannotKillAThreadTheProxyNeverGave(t *testing.T) {
	m := startOrdersMirror(t)

	_, err := m.MySQL().ConnectReference().Execute("KILL QUERY 3999999999")
	assert.EqualError(t, err, "ERROR 1094 (HY000): Unknown thread id: 3999999999")
}

func TestMySQLReferenceSeesTheDatabaseVersion(t *testing.T) {
	m := startOrdersMirror(t)

	version := ""
	assert.NoError(t, m.MySQL().Server().QueryRowContext(t.Context(), "SELECT VERSION()").Scan(&version))
	assert.Equal(t, version, m.MySQL().ConnectReference().GetServerVersion())
}

func TestMySQLReferenceConnectsWithItsOwnCollationAndFeatures(t *testing.T) {
	m := startOrdersMirror(t)
	for _, test := range []struct {
		name      string
		foundRows bool
		affected  uint64
	}{
		{name: "ChangedRows", affected: 0},
		{name: "FoundRows", foundRows: true, affected: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := m.MySQL().ConnectReference(func(c *client.Conn) error {
				if test.foundRows {
					assert.NoError(t, c.SetCapability(gomysql.CLIENT_FOUND_ROWS))
				}
				return c.SetCollation("utf8mb4_bin")
			})
			result, err := conn.Execute("SELECT @@collation_connection")
			assert.NoError(t, err)
			collation, err := result.GetString(0, 0)
			assert.NoError(t, err)
			assert.Equal(t, "utf8mb4_bin", collation)
			// The update matches a row without changing it.
			result, err = conn.Execute("UPDATE items SET quantity = quantity WHERE id = 3")
			assert.NoError(t, err)
			assert.Equal(t, test.affected, result.AffectedRows)
		})
	}
}

func TestMySQLCandidatePreparedStatementQuarantines(t *testing.T) {
	m := startOrdersMirror(t)

	_, err := m.MySQL().Candidate.PrepareContext(t.Context(), "SELECT sku FROM items WHERE id = ?") //nolint:sqlclosecheck // Preparing fails.
	assert.Contains(t, err.Error(), "SPECTRE: prepared statements are not supported")
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantineUnable, "command", "prepared statements are not supported"))
}

// startOrdersMirror mirrors two copies of an orders service that reads one database.
func startOrdersMirror(t *testing.T, settings ...it.Setting) *it.Mirror {
	t.Helper()
	return it.Start(t, append([]it.Setting{
		it.Schema(ordersSchema),
		it.Scripts(`
			import { ingress } from "spectre";
			import type { MerchantOrders } from "orders";

			ingress.http("GET /merchants/{merchant}/orders").match<MerchantOrders>();
		`),
		it.MySQL(
			"CREATE TABLE orders (id BIGINT PRIMARY KEY, merchant_id BIGINT NOT NULL, label VARCHAR(32) NOT NULL)",
			"CREATE TABLE items (id BIGINT PRIMARY KEY, order_id BIGINT NOT NULL, sku VARCHAR(32), quantity INT NOT NULL)",
			"INSERT INTO orders VALUES (7, 1, 'A'), (8, 1, 'B'), (9, 1, 'C'), (10, 2, 'D')",
			"INSERT INTO items VALUES (1, 7, 'apple', 2), (2, 7, NULL, 1), (3, 9, 'pear', 5), (4, 10, 'plum', 1)",
		),
		it.Reference(ordersService()),
		it.Candidate(ordersService()),
	}, settings...)...)
}

// addItem adds an item to order 8 in a transaction of the test's mirrored request, and returns
// the rows it added. Its COMMIT names no request, as one a driver sends itself does.
func addItem(ctx context.Context, database *sql.DB) (int64, error) {
	conn, err := database.Conn(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "connect")
	}
	defer conn.Close()
	// The driver's own START TRANSACTION would name no request, so the test sends its own.
	if _, err := conn.ExecContext(ctx, it.InTestRequest("START TRANSACTION")); err != nil {
		return 0, errors.Wrap(err, "begin")
	}
	result, err := conn.ExecContext(ctx, it.InTestRequest("INSERT INTO items (id, order_id, sku, quantity) VALUES (?, ?, ?, ?)"), 5, 8, "fig", 3)
	if err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return 0, errors.Wrap(err, "insert item")
	}
	added, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "count added items")
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return added, errors.Wrap(err, "commit")
}

// begin starts a transaction of the test's mirrored request on a connection of its own. The
// driver's own START TRANSACTION would name no request, so the test sends its own.
func begin(t *testing.T, database *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := database.Conn(t.Context())
	assert.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.ExecContext(t.Context(), it.InTestRequest("START TRANSACTION"))
	assert.NoError(t, err)
	return conn
}

// commit commits a transaction without naming a request, as a driver does.
func commit(t *testing.T, transaction *sql.Conn) {
	t.Helper()
	_, err := transaction.ExecContext(t.Context(), "COMMIT")
	assert.NoError(t, err)
}

// order is a merchant's order with its items, as the orders service returns it.
type order struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
	Items []item `json:"items,omitempty"`
}

type item struct {
	ID       int64          `json:"id"`
	SKU      Option[string] `json:"sku,omitzero"`
	Quantity int64          `json:"quantity"`
}

// ordersService serves a merchant's orders, read from the mirror's database with a
// statement for the orders and one for the items of each order, each tagged with the request's ID.
func ordersService() it.Service {
	return func(t *testing.T, backend it.Backend) {
		t.Helper()
		database := backend.MySQL.MustGet()
		backend.HTTP.HandleFunc("GET /merchants/{merchant}/orders", func(writer http.ResponseWriter, request *http.Request) {
			merchant, err := strconv.ParseInt(request.PathValue("merchant"), 10, 64)
			if err != nil {
				http.Error(writer, "merchant must be a number", http.StatusBadRequest)
				return
			}
			orders, err := selectOrders(request.Context(), database, it.RequestTagger(request), merchant)
			if err != nil {
				http.Error(writer, "orders cannot be read", http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string][]order{"orders": orders})
		})
	}
}

// joinedOrdersService serves a merchant's orders as ordersService does, read with one
// statement that joins the orders to their items.
func joinedOrdersService() it.Service {
	return func(t *testing.T, backend it.Backend) {
		t.Helper()
		database := backend.MySQL.MustGet()
		backend.HTTP.HandleFunc("GET /merchants/{merchant}/orders", func(writer http.ResponseWriter, request *http.Request) {
			orders, err := selectJoinedOrders(request.Context(), database, it.RequestTagger(request), request.PathValue("merchant"))
			if err != nil {
				http.Error(writer, "orders cannot be read", http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string][]order{"orders": orders})
		})
	}
}

// selectJoinedOrders selects the orders with their items in one statement.
func selectJoinedOrders(ctx context.Context, database *sql.DB, tag func(statement string) string, merchant string) ([]order, error) {
	rows, err := database.QueryContext(ctx, tag("SELECT o.id, o.label, i.id AS item, i.sku, i.quantity FROM orders o "+
		"LEFT JOIN items i ON i.order_id = o.id WHERE o.merchant_id = ? ORDER BY o.id, i.id"), merchant)
	if err != nil {
		return nil, errors.Wrap(err, "select orders")
	}
	defer rows.Close()
	orders := []order{}
	for rows.Next() {
		read, itemID, sku, quantity := order{}, sql.NullInt64{}, sql.NullString{}, sql.NullInt64{}
		if err := rows.Scan(&read.ID, &read.Label, &itemID, &sku, &quantity); err != nil {
			return nil, errors.Wrap(err, "read order")
		}
		if len(orders) == 0 || orders[len(orders)-1].ID != read.ID {
			orders = append(orders, read)
		}
		if itemID.Valid {
			found := item{ID: itemID.Int64, Quantity: quantity.Int64}
			if sku.Valid {
				found.SKU = Some(sku.String)
			}
			orders[len(orders)-1].Items = append(orders[len(orders)-1].Items, found)
		}
	}
	return orders, errors.Wrap(rows.Err(), "read orders")
}

// startStaffMirror mirrors a database of employees, their managers, and their departments.
// The services do nothing; the tests send each side's statements themselves.
func startStaffMirror(t *testing.T, script string) *it.Mirror {
	t.Helper()
	return it.Start(t,
		it.Scripts(script),
		it.MySQL(
			"CREATE TABLE departments (id BIGINT PRIMARY KEY, name VARCHAR(32) NOT NULL)",
			"CREATE TABLE employees (id BIGINT PRIMARY KEY, name VARCHAR(32) NOT NULL, manager_id BIGINT, department_id BIGINT NOT NULL)",
			"INSERT INTO departments VALUES (1, 'Engineering'), (2, 'Sales')",
			"INSERT INTO employees VALUES (1, 'Ada', NULL, 1), (2, 'Grace', 1, 1), (3, 'Linus', 2, 1), (4, 'Mary', 1, 2)",
		),
		it.Reference(idleService), it.Candidate(idleService),
	)
}

// querier runs statements, as a side's handle or a connection to the database does.
type querier interface {
	QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error)
}

// queried runs a statement of the test's mirrored request, and describes its reply.
func queried(t *testing.T, database querier, statement string, arguments ...any) ([]columnType, [][]Option[string]) {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), it.InTestRequest(statement), arguments...)
	assert.NoError(t, err)
	return described(t, rows)
}

// databaseOf connects to the mirror's database itself, bypassing the proxy.
func databaseOf(t *testing.T, m *it.Mirror) *sql.Conn {
	t.Helper()
	server, err := m.MySQL().Server().Conn(t.Context())
	assert.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	_, err = server.ExecContext(t.Context(), "USE `"+m.MySQL().Name+"`")
	assert.NoError(t, err)
	return server
}

// columnType is what a driver learns of a reply column.
type columnType struct {
	Name, Type       string
	Nullable         bool
	Precision, Scale int64
	Length           int64
}

// described reads every row of rows as text, and describes its columns.
func described(t *testing.T, rows *sql.Rows) ([]columnType, [][]Option[string]) {
	t.Helper()
	defer rows.Close()
	types, err := rows.ColumnTypes()
	assert.NoError(t, err)
	columns := []columnType{}
	for _, column := range types {
		nullable, _ := column.Nullable()
		precision, scale, _ := column.DecimalSize()
		length, _ := column.Length()
		columns = append(columns, columnType{Name: column.Name(), Type: column.DatabaseTypeName(), Nullable: nullable,
			Precision: precision, Scale: scale, Length: length})
	}
	read := [][]Option[string]{}
	for rows.Next() {
		values := make([]sql.NullString, len(types))
		pointers := make([]any, len(types))
		for index := range values {
			pointers[index] = &values[index]
		}
		assert.NoError(t, rows.Scan(pointers...))
		row := []Option[string]{}
		for _, value := range values {
			if value.Valid {
				row = append(row, Some(value.String))
			} else {
				row = append(row, None[string]())
			}
		}
		read = append(read, row)
	}
	assert.NoError(t, rows.Err())
	return columns, read
}

// orderTotal is how many items an order has, and their total quantity.
type orderTotal struct {
	Order    int64 `json:"order"`
	Lines    int64 `json:"lines"`
	Quantity int64 `json:"quantity"`
}

// totalsService serves the totals of a merchant's orders that have items, read by totals.
func totalsService(totals func(ctx context.Context, database *sql.DB, tag func(statement string) string, merchant string) ([]orderTotal, error)) it.Service {
	return func(t *testing.T, backend it.Backend) {
		t.Helper()
		database := backend.MySQL.MustGet()
		backend.HTTP.HandleFunc("GET /merchants/{merchant}/totals", func(writer http.ResponseWriter, request *http.Request) {
			read, err := totals(request.Context(), database, it.RequestTagger(request), request.PathValue("merchant"))
			if err != nil {
				http.Error(writer, "totals cannot be read", http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string][]orderTotal{"totals": read})
		})
	}
}

// totalsGroupedInCode reads every item's quantity, and adds them up by order.
func totalsGroupedInCode(ctx context.Context, database *sql.DB, tag func(statement string) string, merchant string) ([]orderTotal, error) {
	rows, err := database.QueryContext(ctx,
		tag("SELECT i.order_id, i.quantity FROM items i JOIN orders o ON o.id = i.order_id WHERE o.merchant_id = ?"), merchant)
	if err != nil {
		return nil, errors.Wrap(err, "select quantities")
	}
	defer rows.Close()
	byOrder := map[int64]*orderTotal{}
	for rows.Next() {
		order, quantity := int64(0), int64(0)
		if err := rows.Scan(&order, &quantity); err != nil {
			return nil, errors.Wrap(err, "read quantity")
		}
		if _, ok := byOrder[order]; !ok {
			byOrder[order] = &orderTotal{Order: order}
		}
		byOrder[order].Lines++
		byOrder[order].Quantity += quantity
	}
	totals := []orderTotal{}
	for _, order := range slices.Sorted(maps.Keys(byOrder)) {
		totals = append(totals, *byOrder[order])
	}
	return totals, errors.Wrap(rows.Err(), "read quantities")
}

// totalsGroupedInSQL reads the totals grouped by the database.
func totalsGroupedInSQL(ctx context.Context, database *sql.DB, tag func(statement string) string, merchant string) ([]orderTotal, error) {
	rows, err := database.QueryContext(ctx, tag("SELECT i.order_id, COUNT(*) AS line_count, SUM(i.quantity) AS total FROM items i "+
		"JOIN orders o ON o.id = i.order_id WHERE o.merchant_id = ? GROUP BY i.order_id ORDER BY i.order_id"), merchant)
	if err != nil {
		return nil, errors.Wrap(err, "select totals")
	}
	defer rows.Close()
	totals := []orderTotal{}
	for rows.Next() {
		read := orderTotal{}
		if err := rows.Scan(&read.Order, &read.Lines, &read.Quantity); err != nil {
			return nil, errors.Wrap(err, "read total")
		}
		totals = append(totals, read)
	}
	return totals, errors.Wrap(rows.Err(), "read totals")
}

// selectOrders selects the orders, then the items of each order with a statement of its own.
func selectOrders(ctx context.Context, database *sql.DB, tag func(statement string) string, merchant int64) ([]order, error) {
	rows, err := database.QueryContext(ctx, tag("SELECT id, label FROM orders WHERE merchant_id = ? ORDER BY id"), merchant)
	if err != nil {
		return nil, errors.Wrap(err, "select orders")
	}
	defer rows.Close()
	orders := []order{}
	for rows.Next() {
		read := order{}
		if err := rows.Scan(&read.ID, &read.Label); err != nil {
			return nil, errors.Wrap(err, "read order")
		}
		orders = append(orders, read)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "read orders")
	}
	for index := range orders {
		items, err := selectItemsOf(ctx, database, tag, orders[index].ID)
		if err != nil {
			return nil, err
		}
		orders[index].Items = items
	}
	return orders, nil
}

func selectItemsOf(ctx context.Context, database *sql.DB, tag func(statement string) string, orderID int64) ([]item, error) {
	rows, err := database.QueryContext(ctx, tag("SELECT id, sku, quantity FROM items WHERE order_id = ? ORDER BY id"), orderID)
	if err != nil {
		return nil, errors.Wrap(err, "select items")
	}
	defer rows.Close()
	items := []item{}
	for rows.Next() {
		read, sku := item{}, sql.NullString{}
		if err := rows.Scan(&read.ID, &sku, &read.Quantity); err != nil {
			return nil, errors.Wrap(err, "read item")
		}
		if sku.Valid {
			read.SKU = Some(sku.String)
		}
		items = append(items, read)
	}
	return items, errors.Wrap(rows.Err(), "read items")
}

// command sends a command without a statement on conn, and returns the first byte of the reply.
func command(t *testing.T, conn *client.Conn, code byte, arguments ...byte) byte {
	t.Helper()
	conn.ResetSequence()
	assert.NoError(t, conn.WritePacket(append([]byte{0, 0, 0, 0, code}, arguments...)))
	reply, err := conn.ReadPacket()
	assert.NoError(t, err)
	return reply[0]
}

// idleService is a service that the test itself stands in for.
func idleService(*testing.T, it.Backend) {}

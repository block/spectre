//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/integration/it"
	"github.com/block/spectre/internal/model"
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
	_, err := selectItemsOf(t.Context(), m.MySQL().Candidate, 99)
	assert.Contains(t, err.Error(), "SPECTRE: no matching reference reply")
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantineUnmatched, "SELECT", "no matching reference request within 500ms"))
	m.MySQL().ExpectStatements(map[string]uint64{})
}

func TestMySQLReferenceStatementTheCandidateNeverRanQuarantines(t *testing.T) {
	m := startOrdersMirror(t)

	// The reference reads order 7's items, and the candidate never asks for them.
	items, err := selectItemsOf(t.Context(), m.MySQL().Reference, 7)
	assert.NoError(t, err)
	assert.Equal(t, []item{{ID: 1, SKU: Some("apple"), Quantity: 2}, {ID: 2, Quantity: 1}}, items)
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantineUnmatched, "SELECT", "no matching candidate request within 500ms"))
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

func TestMySQLCandidateReplaysTheReferenceDatabaseError(t *testing.T) {
	m := startOrdersMirror(t)

	for _, database := range []*sql.DB{m.MySQL().Reference, m.MySQL().Candidate} {
		_, err := database.ExecContext(t.Context(), "SELECT id FROM missing")
		assert.Contains(t, err.Error(), "Error 1146 (42S02): Table '"+m.MySQL().Name+".missing' doesn't exist")
	}
	m.MySQL().ExpectStatements(map[string]uint64{"SELECT `id` FROM `missing`": 1})
}

func TestMySQLReferenceAuthenticatesWithTheDatabase(t *testing.T) {
	m := startOrdersMirror(t)

	err := m.MySQL().OpenReference("root", "wrong").PingContext(t.Context())
	assert.Contains(t, err.Error(), "Error 1045 (28000): Access denied for user 'root'")
}

func TestMySQLReferencePreparedStatementsOnlyReachTheDatabase(t *testing.T) {
	m := startOrdersMirror(t)

	// Prepared statements are forwarded, and offer nothing for the candidate to match.
	prepared, err := m.MySQL().Reference.PrepareContext(t.Context(), "SELECT sku FROM items WHERE id = ?")
	assert.NoError(t, err)
	defer prepared.Close()
	var sku string
	assert.NoError(t, prepared.QueryRowContext(t.Context(), 3).Scan(&sku))
	assert.Equal(t, "pear", sku)

	// The candidate is not quarantined, so ingress still compares a call.
	m.Ingress.Get("/merchants/1/orders").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneOrders)
	m.ExpectCalls(it.Calls{merchantOneOrdersRoute: 1})
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

// addItem adds an item to order 8 in a transaction, and returns the rows it added.
func addItem(ctx context.Context, database *sql.DB) (int64, error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, errors.Wrap(err, "begin")
	}
	defer transaction.Rollback() //nolint:errcheck // Rolling back after a commit does nothing.
	result, err := transaction.ExecContext(ctx, "INSERT INTO items (id, order_id, sku, quantity) VALUES (?, ?, ?, ?)", 5, 8, "fig", 3)
	if err != nil {
		return 0, errors.Wrap(err, "insert item")
	}
	added, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "count added items")
	}
	return added, errors.Wrap(transaction.Commit(), "commit")
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
// statement for the orders and one for the items of each order.
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
			orders, err := selectOrders(request.Context(), database, merchant)
			if err != nil {
				http.Error(writer, "orders cannot be read", http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string][]order{"orders": orders})
		})
	}
}

// selectOrders selects the orders, then the items of each order with a statement of its own.
func selectOrders(ctx context.Context, database *sql.DB, merchant int64) ([]order, error) {
	rows, err := database.QueryContext(ctx, "SELECT id, label FROM orders WHERE merchant_id = ? ORDER BY id", merchant)
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
		items, err := selectItemsOf(ctx, database, orders[index].ID)
		if err != nil {
			return nil, err
		}
		orders[index].Items = items
	}
	return orders, nil
}

func selectItemsOf(ctx context.Context, database *sql.DB, orderID int64) ([]item, error) {
	rows, err := database.QueryContext(ctx, "SELECT id, sku, quantity FROM items WHERE order_id = ? ORDER BY id", orderID)
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

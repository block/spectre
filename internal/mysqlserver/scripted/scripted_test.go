package scripted_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/mysqlserver/exact"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
	"github.com/block/spectre/internal/mysqlserver/scripted"
)

const (
	ordersStatement   = "SELECT id, label FROM orders WHERE merchant_id = ? ORDER BY id"
	itemsStatement    = "SELECT id, sku FROM items WHERE order_id = ? ORDER BY id"
	joinOfMerchantOne = "SELECT o.id, o.label, i.id AS item, i.sku FROM orders o LEFT JOIN items i ON i.order_id = o.id WHERE o.merchant_id = 1 ORDER BY o.id, i.id"
	joinStatement     = "SELECT o.id, o.label, i.id AS item, i.sku FROM orders o LEFT JOIN items i ON i.order_id = o.id WHERE o.merchant_id = ? ORDER BY o.id, i.id"
)

const combineScript = `
	egress.mysql.statements("` + joinStatement + `").replaces("` + ordersStatement + `", "` + itemsStatement + `").using(
		([merchant], [orders, items]) => orders([merchant]).flatMap((order) => {
			const found = items([order.id]);
			if (found.length === 0) return [{ "o.id": order.id, "o.label": order.label, item: null, "i.sku": null }];
			return found.map((item) => ({ "o.id": order.id, "o.label": order.label, item: item.id, "i.sku": item.sku }));
		}),
	);
`

const splitScript = `
	egress.mysql.statements("` + ordersStatement + `", "` + itemsStatement + `").replaces("` + joinStatement + `").using(
		([merchant], rows, [orders, items]) => {
			const labels = new Map<string, string | null>();
			const found = new Map<string, Row[]>();
			for (const row of rows) {
				labels.set(row["o.id"]!, row["o.label"]);
				const list = found.get(row["o.id"]!) ?? [];
				if (row.item !== null) list.push({ id: row.item, sku: row["i.sku"] });
				found.set(row["o.id"]!, list);
			}
			orders([merchant], [...labels].map(([id, label]) => ({ id, label })));
			for (const [order, list] of found) items([order], list);
		},
	);
`

// key is how MySQL flags a primary key column.
const key = mysql.PRI_KEY_FLAG | mysql.NOT_NULL_FLAG

var (
	orderColumns = []normaliser.Column{
		{Schema: "shop", Table: "orders", OrgTable: "orders", Name: "id", OrgName: "id", Charset: 63, Length: 20, Type: mysql.MYSQL_TYPE_LONGLONG, Flags: key},
		{Schema: "shop", Table: "orders", OrgTable: "orders", Name: "label", OrgName: "label", Charset: 255, Length: 512, Type: mysql.MYSQL_TYPE_VAR_STRING},
	}
	itemColumns = []normaliser.Column{
		{Schema: "shop", Table: "items", OrgTable: "items", Name: "id", OrgName: "id", Charset: 63, Length: 20, Type: mysql.MYSQL_TYPE_LONGLONG, Flags: key},
		{Schema: "shop", Table: "items", OrgTable: "items", Name: "sku", OrgName: "sku", Charset: 255, Length: 512, Type: mysql.MYSQL_TYPE_VAR_STRING},
	}
	// The LEFT JOIN may find no item, so MySQL sends the item's key as nullable.
	joinColumns = []normaliser.Column{
		renamed(orderColumns[0], "o", "id"), renamed(orderColumns[1], "o", "label"),
		nullable(renamed(itemColumns[0], "i", "item")), renamed(itemColumns[1], "i", "sku"),
	}
	orders = normaliser.Rows{Columns: orderColumns, Rows: [][]normaliser.Value{{Some("7"), Some("first")}, {Some("8"), None[string]()}}}
	join   = normaliser.Rows{Columns: joinColumns, Rows: [][]normaliser.Value{
		{Some("7"), Some("first"), Some("1"), Some("apple")},
		{Some("7"), Some("first"), Some("2"), None[string]()},
		{Some("8"), None[string](), None[string](), None[string]()},
	}}
)

// The reference reads a merchant's orders, then each order's items; the candidate reads
// them with one JOIN, answered from the reference's replies with their column types.
func TestCandidateJoinCombinesReferenceStatements(t *testing.T) {
	statements := newNormaliser(t, combineScript)
	engine := newEngine()
	reference := statements.NewSession()
	engine.offer(t, reference, "SELECT id, label FROM orders WHERE merchant_id = 1 ORDER BY id", orders)
	engine.offer(t, reference, "select id, sku from items where order_id=7 order by id -- items of 7",
		normaliser.Rows{Columns: itemColumns, Rows: [][]normaliser.Value{{Some("1"), Some("apple")}, {Some("2"), None[string]()}}})
	engine.offer(t, reference, "SELECT id, sku FROM items WHERE order_id = 8 ORDER BY id", normaliser.Rows{Columns: itemColumns})

	reply, err := statements.NewSession().Candidate(t.Context(), parse(joinOfMerchantOne), engine.claim)
	assert.NoError(t, err)

	assert.Equal(t, normaliser.Reply(join), reply)
	assert.Equal(t, 0, len(engine.units))
}

// The reference reads a merchant's orders and items with one JOIN; the candidate reads the
// orders, then each order's items, each answered from the split JOIN.
func TestReferenceJoinSplitsForCandidateStatements(t *testing.T) {
	statements := newNormaliser(t, splitScript)
	engine := newEngine()
	engine.offer(t, statements.NewSession(), joinOfMerchantOne, join)
	candidate := statements.NewSession()

	replies := []normaliser.Reply{}
	for _, sql := range []string{
		"SELECT id, label FROM orders WHERE merchant_id = 1 ORDER BY id",
		"SELECT id, sku FROM items WHERE order_id = 7 ORDER BY id",
		"select id, sku from items where order_id = 8 order by id",
	} {
		reply, err := candidate.Candidate(t.Context(), parse(sql), engine.claim)
		assert.NoError(t, err)
		replies = append(replies, reply)
	}

	assert.Equal(t, []normaliser.Reply{
		orders,
		normaliser.Rows{Columns: itemColumns, Rows: [][]normaliser.Value{{Some("1"), Some("apple")}, {Some("2"), None[string]()}}},
		normaliser.Rows{Columns: itemColumns, Rows: [][]normaliser.Value{}},
	}, replies)
	assert.Equal(t, 0, len(engine.units))
}

// The candidate reads the items of an order the reference's JOIN did not include.
func TestStatementsWithOtherValuesDoNotPair(t *testing.T) {
	statements := newNormaliser(t, splitScript)
	engine := newEngine()
	engine.offer(t, statements.NewSession(), joinOfMerchantOne, join)

	_, err := statements.NewSession().Candidate(t.Context(), parse("SELECT id, sku FROM items WHERE order_id = 9 ORDER BY id"), engine.claim)

	assert.IsError(t, err, errUnmatched)
}

// The services run a statement that no script names, alongside a replacement.
func TestOtherStatementsPairExactly(t *testing.T) {
	statements := newNormaliser(t, splitScript)
	engine := newEngine()
	engine.offer(t, statements.NewSession(), "SELECT 1", normaliser.OK{})

	reply, err := statements.NewSession().Candidate(t.Context(), parse("select 1"), engine.claim)
	assert.NoError(t, err)

	assert.Equal(t, normaliser.Reply(normaliser.OK{}), reply)
}

// The script throws while answering the candidate's JOIN.
func TestScriptFailuresCannotBeAnswered(t *testing.T) {
	statements := newNormaliser(t, `
		egress.mysql.statements("`+joinStatement+`").replaces("`+ordersStatement+`").using(() => { throw new Error("boom"); });
	`)

	_, err := statements.NewSession().Candidate(t.Context(), parse(joinOfMerchantOne), newEngine().claim)

	_, unsupported := errors.AsType[normaliser.UnsupportedError](err)
	assert.True(t, unsupported)
}

// A script names a statement whose reply columns SPECTRE cannot know, or cannot tell apart.
func TestPatternsMustNameTheirColumns(t *testing.T) {
	for name, test := range map[string]struct {
		statement string
		message   string
	}{
		"Wildcard":  {statement: "SELECT * FROM orders WHERE id = ?", message: "selects *, so its columns are unknown"},
		"NotSelect": {statement: "DELETE FROM orders WHERE id = ?", message: "is not a SELECT statement"},
		"Several":   {statement: "SELECT 1; SELECT 2", message: "is not one statement"},
		"SameLabel": {
			statement: "SELECT e.name AS name, m.name AS name FROM employees e JOIN employees m ON m.id = e.manager_id WHERE e.id = ?",
			message:   `selects two columns labelled "name"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := scripted.New(scripts(t, `egress.mysql.statements("`+test.statement+`").replaces("SELECT 1").using(() => []);`), exact.New())
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

// The reference reads every item of a merchant and totals them in code; the candidate runs a
// GROUP BY, whose aggregates get the column types MySQL would give them.
func TestAggregatesAreTypedAsMySQLTypesThem(t *testing.T) {
	const (
		quantities = "SELECT i.order_id, i.id, i.sku, i.quantity FROM items i JOIN orders o ON o.id = i.order_id WHERE o.merchant_id = ?"
		totals     = "SELECT i.order_id, COUNT(*) AS line_count, SUM(i.quantity) AS total, AVG(i.quantity) AS average, " +
			"MIN(i.sku) AS first_sku, MAX(i.id) AS last_item, SUM(i.sku) AS sku_sum " +
			"FROM items i JOIN orders o ON o.id = i.order_id WHERE o.merchant_id = ? GROUP BY i.order_id"
	)
	statements := newNormaliser(t, `
		egress.mysql.statements("`+totals+`").replaces("`+quantities+`").using(([merchant], [quantitiesOf]) =>
		  quantitiesOf([merchant]).map((row) => ({
		    "i.order_id": row["i.order_id"], line_count: "1", total: row["i.quantity"], average: row["i.quantity"] + ".0000",
		    first_sku: row["i.sku"], last_item: row["i.id"], sku_sum: "0",
		  })));
	`)
	// The columns MySQL 8 sends for the quantities statement.
	noDefault := uint16(4096)
	orderID := normaliser.Column{Schema: "shop", Table: "i", OrgTable: "items", Name: "order_id", OrgName: "order_id", Charset: 63, Length: 20,
		Type: mysql.MYSQL_TYPE_LONGLONG, Flags: mysql.NOT_NULL_FLAG | noDefault}
	columns := []normaliser.Column{
		orderID,
		renamed(itemColumns[0], "i", "id"),
		renamed(itemColumns[1], "i", "sku"),
		{Schema: "shop", Table: "i", OrgTable: "items", Name: "quantity", OrgName: "quantity", Charset: 63, Length: 11,
			Type: mysql.MYSQL_TYPE_LONG, Flags: mysql.NOT_NULL_FLAG | noDefault},
	}
	columns[1].Flags |= noDefault
	engine := newEngine()
	engine.offer(t, statements.NewSession(), strings.ReplaceAll(quantities, "?", "1"), normaliser.Rows{Columns: columns,
		Rows: [][]normaliser.Value{{Some("7"), Some("1"), Some("apple"), Some("2")}}})

	reply, err := statements.NewSession().Candidate(t.Context(), parse(strings.ReplaceAll(totals, "?", "1")), engine.claim)
	assert.NoError(t, err)

	rows, ok := reply.(normaliser.Rows)
	assert.True(t, ok)
	// As MySQL describes the same GROUP BY: aggregates name no table, and MIN and MAX drop key flags.
	assert.Equal(t, []normaliser.Column{
		orderID,
		{Name: "line_count", Charset: 63, Length: 21, Type: mysql.MYSQL_TYPE_LONGLONG, Flags: mysql.NOT_NULL_FLAG},
		{Name: "total", Charset: 63, Length: 33, Type: mysql.MYSQL_TYPE_NEWDECIMAL},
		{Name: "average", Charset: 63, Length: 16, Type: mysql.MYSQL_TYPE_NEWDECIMAL, Flags: mysql.BINARY_FLAG, Decimals: 4},
		{Name: "first_sku", Charset: 255, Length: 512, Type: mysql.MYSQL_TYPE_VAR_STRING},
		{Name: "last_item", Charset: 63, Length: 20, Type: mysql.MYSQL_TYPE_LONGLONG, Flags: noDefault},
		{Name: "sku_sum", Charset: 255, Type: mysql.MYSQL_TYPE_VAR_STRING},
	}, rows.Columns)
}

// The candidate also runs, verbatim, a statement that a replacement names on the other
// side, such as the reference's per-order items statement on another endpoint.
func TestStatementsOfAPatternPairOnEitherSide(t *testing.T) {
	for name, test := range map[string]struct {
		script string
		sql    string
		reply  normaliser.Rows
	}{
		"ReferenceStatementOfACombination": {
			script: combineScript,
			sql:    "SELECT id, label FROM orders WHERE merchant_id = 1 ORDER BY id",
			reply:  orders,
		},
		"CandidateStatementOfASplit": {
			script: splitScript,
			sql:    "SELECT id, sku FROM items WHERE order_id = 7 ORDER BY id",
			reply:  normaliser.Rows{Columns: itemColumns, Rows: [][]normaliser.Value{{Some("1"), Some("apple")}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			statements := newNormaliser(t, test.script)
			engine := newEngine()
			engine.offer(t, statements.NewSession(), test.sql, test.reply)

			reply, err := statements.NewSession().Candidate(t.Context(), parse(strings.ToLower(test.sql)+" -- candidate"), engine.claim)
			assert.NoError(t, err)

			assert.Equal(t, normaliser.Reply(test.reply), reply)
			assert.Equal(t, 0, len(engine.units))
		})
	}
}

// The reference reads a department's employees, its name, then each manager; the candidate
// joins employees to their managers and department, and may find no manager.
func TestOuterJoinedColumnsMayBeNull(t *testing.T) {
	const (
		employees  = "SELECT id, name, manager_id FROM employees WHERE department_id = ? ORDER BY id"
		department = "SELECT name FROM departments WHERE id = ?"
		employee   = "SELECT id, name FROM employees WHERE id = ?"
		joined     = "SELECT e.id, e.name, m.name AS manager, d.name AS department FROM employees e " +
			"LEFT JOIN employees m ON m.id = e.manager_id JOIN departments d ON d.id = e.department_id WHERE e.department_id = ? ORDER BY e.id"
	)
	statements := newNormaliser(t, `
		egress.mysql.statements("`+joined+`").replaces("`+employees+`", "`+department+`", "`+employee+`").using(
		  ([id], [inDepartment, named, byID]) => {
		    const [{ name }] = named([id]);
		    return inDepartment([id]).map((row) => ({
		      "e.id": row.id, "e.name": row.name, department: name,
		      manager: row.manager_id === null ? null : byID([row.manager_id])[0]!.name,
		    }));
		  });
	`)
	engine := newEngine()
	reference := statements.NewSession()
	engine.offer(t, reference, strings.ReplaceAll(employees, "?", "1"), normaliser.Rows{
		Columns: employeeColumns, Rows: [][]normaliser.Value{{Some("1"), Some("Ada"), None[string]()}, {Some("2"), Some("Grace"), Some("1")}},
	})
	engine.offer(t, reference, strings.ReplaceAll(department, "?", "1"), normaliser.Rows{
		Columns: []normaliser.Column{departmentName}, Rows: [][]normaliser.Value{{Some("Engineering")}},
	})
	engine.offer(t, reference, strings.ReplaceAll(employee, "?", "1"), normaliser.Rows{
		Columns: employeeColumns[:2], Rows: [][]normaliser.Value{{Some("1"), Some("Ada")}},
	})

	reply, err := statements.NewSession().Candidate(t.Context(), parse(strings.ReplaceAll(joined, "?", "1")), engine.claim)
	assert.NoError(t, err)

	// The self join's two employees stay apart by their labels.
	assert.Equal(t, normaliser.Reply(normaliser.Rows{
		Columns: []normaliser.Column{
			renamed(employeeColumns[0], "e", "id"), renamed(employeeColumns[1], "e", "name"),
			nullable(renamed(employeeColumns[1], "m", "manager")), renamed(departmentName, "d", "department"),
		},
		Rows: [][]normaliser.Value{{Some("1"), Some("Ada"), None[string](), Some("Engineering")}, {Some("2"), Some("Grace"), Some("Ada"), Some("Engineering")}},
	}), reply)
	assert.Equal(t, 0, len(engine.units))
}

// The reference joins employees to their managers; the candidate reads the employees, then
// each manager, whose columns MySQL defines as the table does, not as the join does.
func TestSplitRepliesDefineColumnsAsTheirTables(t *testing.T) {
	const (
		joined    = "SELECT m.id AS manager_id, m.name AS manager, e.id, e.name FROM employees e LEFT JOIN employees m ON m.id = e.manager_id WHERE e.department_id = ?"
		employees = "SELECT id, name FROM employees WHERE department_id = ?"
		managers  = "SELECT id, name FROM employees WHERE id = ?"
	)
	statements := newNormaliser(t, `
		egress.mysql.statements("`+employees+`", "`+managers+`").replaces("`+joined+`").using(([id], rows, [inDepartment, byID]) => {
		  inDepartment([id], rows.map((row) => ({ id: row["e.id"], name: row["e.name"] })));
		  for (const row of rows) if (row.manager_id !== null) byID([row.manager_id], [{ id: row.manager_id, name: row.manager }]);
		});
	`)
	engine := newEngine()
	engine.offer(t, statements.NewSession(), strings.ReplaceAll(joined, "?", "1"), normaliser.Rows{
		Columns: []normaliser.Column{
			nullable(renamed(employeeColumns[0], "m", "manager_id")), nullable(renamed(employeeColumns[1], "m", "manager")),
			renamed(employeeColumns[0], "e", "id"), renamed(employeeColumns[1], "e", "name"),
		},
		Rows: [][]normaliser.Value{{None[string](), None[string](), Some("1"), Some("Ada")}, {Some("1"), Some("Ada"), Some("2"), Some("Grace")}},
	})
	candidate := statements.NewSession()

	replies := []normaliser.Reply{}
	for _, sql := range []string{strings.ReplaceAll(employees, "?", "1"), strings.ReplaceAll(managers, "?", "1")} {
		reply, err := candidate.Candidate(t.Context(), parse(sql), engine.claim)
		assert.NoError(t, err)
		replies = append(replies, reply)
	}

	columns := []normaliser.Column{renamed(employeeColumns[0], "employees", "id"), renamed(employeeColumns[1], "employees", "name")}
	assert.Equal(t, []normaliser.Reply{
		normaliser.Rows{Columns: columns, Rows: [][]normaliser.Value{{Some("1"), Some("Ada")}, {Some("2"), Some("Grace")}}},
		normaliser.Rows{Columns: columns, Rows: [][]normaliser.Value{{Some("1"), Some("Ada")}}},
	}, replies)
}

var (
	employeeColumns = []normaliser.Column{
		{Schema: "staff", Table: "employees", OrgTable: "employees", Name: "id", OrgName: "id", Charset: 63, Length: 20, Type: mysql.MYSQL_TYPE_LONGLONG, Flags: key},
		{Schema: "staff", Table: "employees", OrgTable: "employees", Name: "name", OrgName: "name", Charset: 255, Length: 128,
			Type: mysql.MYSQL_TYPE_VAR_STRING, Flags: mysql.NOT_NULL_FLAG},
		{Schema: "staff", Table: "employees", OrgTable: "employees", Name: "manager_id", OrgName: "manager_id", Charset: 63, Length: 20, Type: mysql.MYSQL_TYPE_LONGLONG},
	}
	departmentName = normaliser.Column{Schema: "staff", Table: "departments", OrgTable: "departments", Name: "name", OrgName: "name",
		Charset: 255, Length: 128, Type: mysql.MYSQL_TYPE_VAR_STRING, Flags: mysql.NOT_NULL_FLAG}
)

// nullable is a column as an outer join sends it, which may find no row.
func nullable(column normaliser.Column) normaliser.Column {
	column.Flags &^= mysql.NOT_NULL_FLAG
	return column
}

func renamed(column normaliser.Column, table, name string) normaliser.Column {
	column.Table, column.Name = table, name
	return column
}

func parse(sql string) normaliser.Command { //nolint:ireturn // Commands are a sum type.
	return normaliser.NewParser().Parse(sql)
}

func newNormaliser(t *testing.T, script string) *scripted.Normaliser {
	t.Helper()
	statements, err := scripted.New(scripts(t, script), exact.New())
	assert.NoError(t, err)
	return statements
}

// scripts loads one script with the spectre module's egress and Row imported.
func scripts(t *testing.T, script string) *comparison.Scripts {
	t.Helper()
	config := comparison.NewConfig()
	config.ScriptsDir, config.Schema.SchemaDir = t.TempDir(), t.TempDir()
	source := `import { egress, type Row } from "spectre";` + script
	assert.NoError(t, os.WriteFile(filepath.Join(config.ScriptsDir, "mysql.ts"), []byte(source), 0o600))
	loaded, err := comparison.New(t.Context(), config)
	assert.NoError(t, err)
	return loaded
}

var errUnmatched = errors.New("no reference unit")

// engine pairs units by route and key in one goroutine, as the egress engine would.
type engine struct {
	units map[string]normaliser.Reply
}

func newEngine() *engine {
	return &engine{units: map[string]normaliser.Reply{}}
}

func (e *engine) offer(t *testing.T, session normaliser.Session, sql string, reply normaliser.Reply) {
	t.Helper()
	units, err := session.Reference(t.Context(), parse(sql), reply)
	assert.NoError(t, err)
	for _, unit := range units {
		e.units[identity(unit.Route, unit.Key)] = unit.Reply
	}
}

func (e *engine) claim(_ context.Context, route model.Route, key any) (normaliser.Reply, error) { //nolint:ireturn // Replies are a sum type.
	identified := identity(route, key)
	reply, ok := e.units[identified]
	if !ok {
		return nil, errors.Wrap(errUnmatched, identified)
	}
	delete(e.units, identified)
	return reply, nil
}

func identity(route model.Route, key any) string {
	encoded, _ := json.Marshal(key) //nolint:errcheck,errchkjson // Keys are JSON values.
	return string(route) + " " + string(encoded)
}

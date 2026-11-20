package scripted_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

var (
	orderColumns = []normaliser.Column{
		{Schema: "shop", Table: "orders", OrgTable: "orders", Name: "id", OrgName: "id", Charset: 63, Length: 20, Type: mysql.MYSQL_TYPE_LONGLONG, Flags: mysql.PRI_KEY_FLAG},
		{Schema: "shop", Table: "orders", OrgTable: "orders", Name: "label", OrgName: "label", Charset: 255, Length: 512, Type: mysql.MYSQL_TYPE_VAR_STRING},
	}
	itemColumns = []normaliser.Column{
		{Schema: "shop", Table: "items", OrgTable: "items", Name: "id", OrgName: "id", Charset: 63, Length: 20, Type: mysql.MYSQL_TYPE_LONGLONG, Flags: mysql.PRI_KEY_FLAG},
		{Schema: "shop", Table: "items", OrgTable: "items", Name: "sku", OrgName: "sku", Charset: 255, Length: 512, Type: mysql.MYSQL_TYPE_VAR_STRING},
	}
	joinColumns = []normaliser.Column{
		renamed(orderColumns[0], "o", "id"), renamed(orderColumns[1], "o", "label"),
		renamed(itemColumns[0], "i", "item"), renamed(itemColumns[1], "i", "sku"),
	}
	orders = normaliser.Rows{Columns: orderColumns, Rows: [][]normaliser.Value{{Some("7"), Some("first")}, {Some("8"), None[string]()}}}
	join   = normaliser.Rows{Columns: joinColumns, Rows: [][]normaliser.Value{
		{Some("7"), Some("first"), Some("1"), Some("apple")},
		{Some("7"), Some("first"), Some("2"), None[string]()},
		{Some("8"), None[string](), None[string](), None[string]()},
	}}
)

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

func TestStatementsWithOtherValuesDoNotPair(t *testing.T) {
	statements := newNormaliser(t, splitScript)
	engine := newEngine()
	engine.offer(t, statements.NewSession(), joinOfMerchantOne, join)

	_, err := statements.NewSession().Candidate(t.Context(), parse("SELECT id, sku FROM items WHERE order_id = 9 ORDER BY id"), engine.claim)

	assert.IsError(t, err, errUnmatched)
}

func TestOtherStatementsPairExactly(t *testing.T) {
	statements := newNormaliser(t, splitScript)
	engine := newEngine()
	engine.offer(t, statements.NewSession(), "SELECT 1", normaliser.OK{})

	reply, err := statements.NewSession().Candidate(t.Context(), parse("select 1"), engine.claim)
	assert.NoError(t, err)

	assert.Equal(t, normaliser.Reply(normaliser.OK{}), reply)
}

func TestScriptFailuresCannotBeAnswered(t *testing.T) {
	statements := newNormaliser(t, `
		egress.mysql.statements("`+joinStatement+`").replaces("`+ordersStatement+`").using(() => { throw new Error("boom"); });
	`)

	_, err := statements.NewSession().Candidate(t.Context(), parse(joinOfMerchantOne), newEngine().claim)

	_, unsupported := errors.AsType[normaliser.UnsupportedError](err)
	assert.True(t, unsupported)
}

func TestPatternsMustNameTheirColumns(t *testing.T) {
	for name, test := range map[string]struct {
		statement string
		message   string
	}{
		"Wildcard":  {statement: "SELECT * FROM orders WHERE id = ?", message: "selects *, so its columns are unknown"},
		"NotSelect": {statement: "DELETE FROM orders WHERE id = ?", message: "is not a SELECT statement"},
		"Several":   {statement: "SELECT 1; SELECT 2", message: "is not one statement"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := scripted.New(scripts(t, `egress.mysql.statements("`+test.statement+`").replaces("SELECT 1").using(() => []);`), exact.New())
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
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

package javascript_test

import (
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
)

const (
	ordersStatement = "SELECT id, label FROM orders WHERE merchant_id = ?"
	itemsStatement  = "SELECT id, sku FROM items WHERE order_id = ?"
	joinStatement   = "SELECT o.id, o.label, i.id AS item, i.sku FROM orders o LEFT JOIN items i ON i.order_id = o.id WHERE o.merchant_id = ?"
)

const rowTypes = `
	interface Order { id: string; label: string | null }
	interface Item { id: string; sku: string | null }
	interface Joined { id: string; label: string | null; item: string | null; sku: string | null }
`

const combineScript = rowTypes + `
	spectre.egress.mysql.statement<Joined>("` + joinStatement + `").replaces<[Order, Item]>("` + ordersStatement + `", "` + itemsStatement + `").using(
		([merchant], [orders, items]) => orders(merchant).flatMap((order): Joined[] => {
			const found = items(Number(order.id));
			if (found.length === 0) return [{ id: order.id, label: order.label, item: null, sku: null }];
			return found.map((item) => ({ id: order.id, label: order.label, item: item.id, sku: item.sku }));
		}),
	);
`

const splitScript = rowTypes + `
	spectre.egress.mysql.statements<[Order, Item]>("` + ordersStatement + `", "` + itemsStatement + `").replaces<Joined>("` + joinStatement + `").using(
		([merchant], rows, [orders, items]) => {
			const seen = new Map<string, Item[]>();
			for (const row of rows) {
				const list = seen.get(row.id) ?? [];
				if (row.item !== null) list.push({ id: row.item, sku: row.sku });
				seen.set(row.id, list);
			}
			orders(merchant, [...seen.keys()].map((id) => ({ id, label: rows.find((row) => row.id === id)!.label })));
			for (const [order, list] of seen) items(order, list);
		},
	);
`

// A script declares a candidate JOIN replacing the reference's N+1 statements, and the
// reverse; each side with one statement decides which way its function runs.
func TestRegistersStatementReplacements(t *testing.T) {
	program, err := newProgram(t, combineScript+strings.Replace(splitScript, rowTypes, "", 1))
	assert.NoError(t, err)

	replacements := program.StatementReplacements()
	assert.Equal(t, 2, len(replacements))
	assert.Equal(t, []string{joinStatement}, replacements[0].Statements())
	assert.Equal(t, []string{ordersStatement, itemsStatement}, replacements[0].Sources())
	assert.True(t, replacements[0].Combines())
	assert.Equal(t, []string{ordersStatement, itemsStatement}, replacements[1].Statements())
	assert.False(t, replacements[1].Combines())
	assert.Equal(t, `egress.mysql.statement("`+joinStatement+`").replaces("`+ordersStatement+`", "`+itemsStatement+`").using()`,
		replacements[0].String())
	assert.Equal(t, `egress.mysql.statements("`+ordersStatement+`", "`+itemsStatement+`").replaces("`+joinStatement+`").using()`,
		replacements[1].String())
}

// The candidate joins orders to items where the reference read the orders, then each
// order's items: the function claims each reference statement and joins their rows.
func TestCombiningClaimsEachSource(t *testing.T) {
	program, err := newProgram(t, combineScript)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	claimed := [][]Option[string]{}
	orders := func(values []Option[string]) ([]javascript.Row, error) {
		claimed = append(claimed, values)
		return []javascript.Row{{"id": Some("7"), "label": Some("first")}, {"id": Some("8"), "label": None[string]()}}, nil
	}
	items := func(values []Option[string]) ([]javascript.Row, error) {
		claimed = append(claimed, values)
		if values[0] == Some("8") {
			return []javascript.Row{}, nil
		}
		return []javascript.Row{{"id": Some("1"), "sku": Some("apple")}}, nil
	}

	rows, err := evaluator.CombineStatements(time.Second, program.StatementReplacements()[0], []Option[string]{Some("1")},
		[]javascript.Claim{orders, items})
	assert.NoError(t, err)

	assert.Equal(t, [][]Option[string]{{Some("1")}, {Some("7")}, {Some("8")}}, claimed)
	assert.Equal(t, []javascript.Row{
		{"id": Some("7"), "label": Some("first"), "item": Some("1"), "sku": Some("apple")},
		{"id": Some("8"), "label": None[string](), "item": None[string](), "sku": None[string]()},
	}, rows)
}

// The reference joins orders to items where the candidate reads the orders, then each
// order's items: the function splits the JOIN into one reply per candidate statement.
func TestSplittingOffersEachReply(t *testing.T) {
	program, err := newProgram(t, splitScript)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	type offer struct {
		index  int
		values []Option[string]
		rows   []javascript.Row
	}
	offers := []offer{}

	err = evaluator.SplitStatement(time.Second, program.StatementReplacements()[0], []Option[string]{Some("1")}, []javascript.Row{
		{"id": Some("7"), "label": Some("first"), "item": Some("1"), "sku": Some("apple")},
		{"id": Some("7"), "label": Some("first"), "item": Some("2"), "sku": None[string]()},
		{"id": Some("8"), "label": Some("second"), "item": None[string](), "sku": None[string]()},
	}, func(index int, values []Option[string], rows []javascript.Row) error {
		offers = append(offers, offer{index, values, rows})
		return nil
	})
	assert.NoError(t, err)

	assert.Equal(t, []offer{
		{0, []Option[string]{Some("1")}, []javascript.Row{{"id": Some("7"), "label": Some("first")}, {"id": Some("8"), "label": Some("second")}}},
		{1, []Option[string]{Some("7")}, []javascript.Row{{"id": Some("1"), "sku": Some("apple")}, {"id": Some("2"), "sku": None[string]()}}},
		{1, []Option[string]{Some("8")}, []javascript.Row{}},
	}, offers)
}

// The reference never ran a statement that the script claims, and the script catches the
// failure; the candidate still cannot be answered with what the script returns.
func TestClaimFailuresEndTheCombinationEvenWhenCaught(t *testing.T) {
	unmatched := errors.New("no reference reply")
	program, err := newProgram(t, `spectre.egress.mysql.statement("SELECT 1").replaces("SELECT 2").using(
		([value], [claim]) => { try { claim(value); } catch { } return []; });`)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()

	_, err = evaluator.CombineStatements(time.Second, program.StatementReplacements()[0], []Option[string]{Some("1")},
		[]javascript.Claim{func([]Option[string]) ([]javascript.Row, error) { return nil, unmatched }})

	assert.IsError(t, err, unmatched)
}

// A script that is broken, returns rows MySQL cannot send, or loops forever, cannot
// answer the candidate.
func TestStatementReplacementFailures(t *testing.T) {
	for name, test := range map[string]struct {
		using string
		err   string
	}{
		"Throws": {
			using: `() => { throw new Error("boom"); }`,
			err:   `JavaScript function of egress.mysql.statement("SELECT 1").replaces("SELECT 2").using() threw an exception`,
		},
		"ReturnsObjects": {
			using: `() => [{ id: {} }] as unknown as spectre.Row[]`,
			err:   `result of egress.mysql.statement("SELECT 1").replaces("SELECT 2").using(): row field "id": value of type map[string]interface {} is not a string, number, boolean or null`,
		},
		"RunsTooLong": {
			using: `() => { for (;;) {} }`,
			err:   `JavaScript function of egress.mysql.statement("SELECT 1").replaces("SELECT 2").using() threw an exception: context deadline exceeded at test.ts:2:71(1)`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			program, err := newProgram(t, `spectre.egress.mysql.statement("SELECT 1").replaces("SELECT 2").using(`+test.using+`);`)
			assert.NoError(t, err)
			evaluator, err := program.NewEvaluator(t.Context())
			assert.NoError(t, err)
			defer evaluator.Close()

			_, err = evaluator.CombineStatements(50*time.Millisecond, program.StatementReplacements()[0], nil, nil)

			assert.EqualError(t, err, test.err)
		})
	}
}

// A script declares a replacement that SPECTRE cannot run, so the scripts fail to load.
func TestRejectsInvalidStatementReplacements(t *testing.T) {
	for name, test := range map[string]struct {
		body    string
		message string
	}{
		"ManyCandidateStatementsCombined": {
			body:    `egress.mysql.statement("SELECT 1", "SELECT 2").replaces("SELECT 3").using(() => []);`,
			message: "spectre.egress.mysql.statement requires one statement",
		},
		"ManyReferenceStatementsSplit": {
			body:    `egress.mysql.statements("SELECT 1", "SELECT 2").replaces("SELECT 3", "SELECT 4").using(() => {});`,
			message: "spectre.EgressMySQLStatements.replaces requires one statement",
		},
		"NoStatements": {
			body:    `egress.mysql.statements();`,
			message: "spectre.egress.mysql.statements requires statements",
		},
		"UsingNotFunction": {
			body:    `egress.mysql.statement("SELECT 1").replaces("SELECT 2").using(1);`,
			message: "spectre.EgressMySQLReplacement.using requires a function",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newProgram(t, `const { egress } = spectre as unknown as Record<string, any>; `+test.body)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

// A script's row types do not fit its statements or its function, so it fails to type-check.
func TestRowTypesCheckReplacements(t *testing.T) {
	for name, test := range map[string]struct {
		body    string
		message string
	}{
		"TooFewStatements": {
			body:    `spectre.egress.mysql.statement("SELECT 1").replaces<[Order, Item]>("SELECT id, label FROM orders").using(() => []);`,
			message: "Source has 1 element(s) but target requires 2.",
		},
		"NonTextField": {
			body:    `spectre.egress.mysql.statement<{ id: number }>("SELECT id FROM orders").replaces("SELECT 2").using(() => []);`,
			message: `not assignable to parameter of type '"row fields must be string | null"'`,
		},
		"UnknownField": {
			body: `spectre.egress.mysql.statement("SELECT 1").replaces<[Order]>("SELECT id, label FROM orders").using(
				([merchant], [orders]) => orders(merchant).map((order) => ({ id: order.ident })));`,
			message: "Property 'ident' does not exist on type 'Order'.",
		},
		"WrongRows": {
			body: `spectre.egress.mysql.statement<Order>("SELECT id, label FROM orders").replaces("SELECT 2").using(
				() => [{ id: "1" }]);`,
			message: "Property 'label' is missing",
		},
		"OfferWithoutRows": {
			body: `spectre.egress.mysql.statements<[Order]>("SELECT id, label FROM orders").replaces("SELECT 2").using(
				([merchant], rows, [orders]) => orders(merchant));`,
			message: "not assignable to parameter of type",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newProgram(t, rowTypes+test.body)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

package scripted_test

import (
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/protocols/mysql/scripted"
	"github.com/block/spectre/internal/schema"
)

// A script declares a candidate JOIN replacing the reference's N+1 statements, and the
// reverse; each side with one statement decides which way its function runs.
func TestRegistersStatementReplacements(t *testing.T) {
	loaded, handle := scripts(t, combineScript+strings.Replace(splitScript, rowTypes, "", 1))

	replacements := handle.Declared(loaded.Program()).Replacements

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

// Egress hashes pairings as the payload type that the binding declares.
func TestDeclaresPayloadTypes(t *testing.T) {
	binding, _ := scripted.NewBinding()
	loaded, err := schema.ParseSources(t.Context(), map[string]string{}, binding.Payloads())
	assert.NoError(t, err)

	_, err = loaded.Type(scripted.Pairing)

	assert.NoError(t, err)
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
			_, _, err := load(t, `const loose = spectre.egress as unknown as Record<string, any>; loose.mysql`+strings.TrimPrefix(test.body, "egress.mysql"), time.Second)
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
			body:    `egress.mysql.statement("SELECT 1").replaces<[Order, Item]>("SELECT id, label FROM orders").using(() => []);`,
			message: "Source has 1 element(s) but target requires 2.",
		},
		"NonTextField": {
			body:    `egress.mysql.statement<{ id: number }>("SELECT id FROM orders").replaces("SELECT 2").using(() => []);`,
			message: `not assignable to parameter of type '"row fields must be string | null"'`,
		},
		"UnknownField": {
			body: `egress.mysql.statement("SELECT 1").replaces<[Order]>("SELECT id, label FROM orders").using(
				([merchant], [orders]) => orders(merchant).map((order) => ({ id: order.ident })));`,
			message: "Property 'ident' does not exist on type 'Order'.",
		},
		"WrongRows": {
			body: `egress.mysql.statement<Order>("SELECT id, label FROM orders").replaces("SELECT 2").using(
				() => [{ id: "1" }]);`,
			message: "Property 'label' is missing",
		},
		"OfferWithoutRows": {
			body: `egress.mysql.statements<[Order]>("SELECT id, label FROM orders").replaces("SELECT 2").using(
				([merchant], rows, [orders]) => orders(merchant));`,
			message: "not assignable to parameter of type",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := load(t, rowTypes+test.body, time.Second)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

package exact_test

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/mysqlserver/exact"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

// claim is one claim a candidate session made.
type claim struct {
	route model.Route
	key   any
}

func TestReferenceOffersTheReplyUnderTheStatementAndDatabase(t *testing.T) {
	session := exact.New().NewSession()
	rows := normaliser.Rows{Columns: []normaliser.Column{{Name: "id"}}, Rows: [][]normaliser.Value{{Some("7")}}}

	units, err := session.Reference(t.Context(), normaliser.UseDatabase{Name: "shop"}, normaliser.OK{})
	assert.NoError(t, err)
	assert.Equal(t, []normaliser.Unit(nil), units)
	units, err = session.Reference(t.Context(), normaliser.Query{SQL: " select id FROM orders"}, rows)
	assert.NoError(t, err)
	assert.Equal(t, []normaliser.Unit{{
		Route: "SELECT",
		Key:   map[string]any{"database": "shop", "statement": "SELECT `id` FROM `orders`"},
		Reply: rows,
	}}, units)
}

func TestReferenceKeepsTheDatabaseThatCouldNotBeSelected(t *testing.T) {
	session := exact.New().NewSession()

	_, err := session.Reference(t.Context(), normaliser.UseDatabase{Name: "missing"}, normaliser.Error{Code: 1049})
	assert.NoError(t, err)
	units, err := session.Reference(t.Context(), normaliser.Query{SQL: "COMMIT"}, normaliser.OK{})
	assert.NoError(t, err)
	assert.Equal(t, []normaliser.Unit{{
		Route: "COMMIT",
		Key:   map[string]any{"database": "", "statement": "COMMIT"},
		Reply: normaliser.OK{},
	}}, units)
}

func TestCandidateReplaysTheClaimedReply(t *testing.T) {
	session := exact.New().NewSession()
	claims := []claim{}
	replay := func(_ context.Context, route model.Route, key any) (normaliser.Reply, error) {
		claims = append(claims, claim{route: route, key: key})
		return normaliser.OK{AffectedRows: 1}, nil
	}

	reply, err := session.Candidate(t.Context(), normaliser.UseDatabase{Name: "shop"}, replay)
	assert.NoError(t, err)
	assert.Equal(t, normaliser.Reply(normaliser.OK{}), reply)
	reply, err = session.Candidate(t.Context(), normaliser.Query{SQL: "(SELECT 1)"}, replay)
	assert.NoError(t, err)
	assert.Equal(t, normaliser.Reply(normaliser.OK{AffectedRows: 1}), reply)
	reply, err = session.Candidate(t.Context(), normaliser.Query{SQL: "/* tag */ SELECT 1"}, replay)
	assert.NoError(t, err)
	assert.Equal(t, normaliser.Reply(normaliser.OK{AffectedRows: 1}), reply)
	assert.Equal(t, []claim{
		{route: "SELECT", key: map[string]any{"database": "shop", "statement": "(SELECT 1)"}},
		{route: "SELECT", key: map[string]any{"database": "shop", "statement": "SELECT 1"}},
	}, claims)
}

func TestUseStatementsSelectTheDatabase(t *testing.T) {
	reference, candidate := exact.New().NewSession(), exact.New().NewSession()
	keys := []any{}
	replay := func(_ context.Context, _ model.Route, key any) (normaliser.Reply, error) {
		keys = append(keys, key)
		return normaliser.OK{}, nil
	}

	for _, use := range []string{
		"USE tenant_a", "use `tenant``b`;", " USE  shop ",
		"USE /* setup */ tenant_c", "USE tenant_d; -- trailing\n", "/* tag */ USE `tenant/*e*/` # note",
	} {
		units, err := reference.Reference(t.Context(), normaliser.Query{SQL: use}, normaliser.OK{})
		assert.NoError(t, err)
		assert.Equal(t, []normaliser.Unit(nil), units)
		units, err = reference.Reference(t.Context(), normaliser.Query{SQL: "SELECT 1"}, normaliser.OK{})
		assert.NoError(t, err)
		keys = append(keys, units[0].Key)
		reply, err := candidate.Candidate(t.Context(), normaliser.Query{SQL: use}, replay)
		assert.NoError(t, err)
		assert.Equal(t, normaliser.Reply(normaliser.OK{}), reply)
		_, err = candidate.Candidate(t.Context(), normaliser.Query{SQL: "SELECT 1"}, replay)
		assert.NoError(t, err)
	}
	expected := []any{}
	for _, database := range []string{"tenant_a", "tenant`b", "shop", "tenant_c", "tenant_d", "tenant/*e*/"} {
		key := map[string]any{"database": database, "statement": "SELECT 1"}
		expected = append(expected, key, key)
	}
	assert.Equal(t, expected, keys)
}

func TestStatementsPairWhenTheyParseAlike(t *testing.T) {
	for _, test := range []struct {
		name                 string
		reference, candidate string
	}{
		{name: "Comments", reference: "SELECT id FROM items /* request=7 */", candidate: "/* app:x */ SELECT /* inner */ id FROM items -- tail"},
		{name: "HashComments", reference: "SELECT 1 # tail", candidate: "SELECT 1"},
		{name: "Spacing", reference: "select  id\n\tfrom items where id=1", candidate: "SELECT id FROM items WHERE id = 1"},
		{name: "Quoting", reference: "SELECT `id` FROM `items` WHERE sku = \"fig\"", candidate: "SELECT id FROM items WHERE sku = 'fig'"},
		{name: "Aliases", reference: "SELECT sku s FROM items i", candidate: "SELECT sku AS s FROM items AS i"},
		{name: "Semicolon", reference: "COMMIT;", candidate: "commit"},
		{name: "Escapes", reference: "SELECT 'it''s'", candidate: "SELECT 'it\\'s'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, keyOf(t, test.reference), keyOf(t, test.candidate))
		})
	}
}

func TestStatementsThatDifferDoNotPair(t *testing.T) {
	for _, test := range []struct {
		name                 string
		reference, candidate string
	}{
		{name: "Values", reference: "SELECT id FROM items WHERE id = 1", candidate: "SELECT id FROM items WHERE id = 2"},
		{name: "Decimals", reference: "SELECT 1.50", candidate: "SELECT 1.5"},
		{name: "Strings", reference: "SELECT 'fig'", candidate: "SELECT 'Fig'"},
		{name: "StringSpacing", reference: "SELECT 'a  b'", candidate: "SELECT 'a b'"},
		{name: "CommentInString", reference: "SELECT 'a/*b*/'", candidate: "SELECT 'a'"},
		{name: "Null", reference: "SELECT NULL", candidate: "SELECT 'NULL'"},
		{name: "Hex", reference: "SELECT 0x0A", candidate: "SELECT 10"},
		{name: "Negation", reference: "SELECT 1--1", candidate: "SELECT 1"},
		{name: "QuotedName", reference: "SELECT `a``b` FROM t", candidate: "SELECT `a` `b` FROM t"},
		{name: "Names", reference: "SELECT id FROM items", candidate: "SELECT id FROM Items"},
		{name: "Hints", reference: "SELECT /*+ MAX_EXECUTION_TIME(10) */ id FROM items", candidate: "SELECT id FROM items"},
		{name: "Locks", reference: "SELECT id FROM items FOR UPDATE", candidate: "SELECT id FROM items"},
		{name: "Statements", reference: "SELECT 1; SELECT 2", candidate: "SELECT 1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.NotEqual(t, keyOf(t, test.reference), keyOf(t, test.candidate))
		})
	}
}

func TestStatementsThatDoNotParsePairByTheirText(t *testing.T) {
	assert.Equal(t, any(map[string]any{"database": "", "statement": "SELEKT 1 /* x */"}), keyOf(t, "SELEKT 1 /* x */"))
}

func TestFailedUseStatementsKeepTheDatabase(t *testing.T) {
	session := exact.New().NewSession()

	_, err := session.Reference(t.Context(), normaliser.UseDatabase{Name: "shop"}, normaliser.OK{})
	assert.NoError(t, err)
	_, err = session.Reference(t.Context(), normaliser.Query{SQL: "USE missing"}, normaliser.Error{Code: 1049})
	assert.NoError(t, err)
	units, err := session.Reference(t.Context(), normaliser.Query{SQL: "SELECT 1"}, normaliser.OK{})
	assert.NoError(t, err)
	assert.Equal(t, any(map[string]any{"database": "shop", "statement": "SELECT 1"}), units[0].Key)
}

// keyOf is the key a reference statement offers its reply under.
func keyOf(t *testing.T, sql string) any {
	t.Helper()
	units, err := exact.New().NewSession().Reference(t.Context(), normaliser.Query{SQL: sql}, normaliser.OK{})
	assert.NoError(t, err)
	assert.Equal(t, 1, len(units))
	return units[0].Key
}

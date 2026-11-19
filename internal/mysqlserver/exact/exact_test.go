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
		Key:   map[string]any{"database": "shop", "statement": " select id FROM orders"},
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
		{route: "statement", key: map[string]any{"database": "shop", "statement": "/* tag */ SELECT 1"}},
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

func TestStatementsArePairedWithTheirComments(t *testing.T) {
	session := exact.New().NewSession()

	units, err := session.Reference(t.Context(), normaliser.Query{SQL: "SELECT 1 /* request=7 */"}, normaliser.OK{})
	assert.NoError(t, err)
	assert.Equal(t, any(map[string]any{"database": "", "statement": "SELECT 1 /* request=7 */"}), units[0].Key)
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

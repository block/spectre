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
	units, err = session.Reference(t.Context(), normaliser.Query{SQL: " select id FROM orders -- trace=1"}, rows)
	assert.NoError(t, err)
	assert.Equal(t, []normaliser.Unit{{
		Route: "SELECT",
		Key:   map[string]any{"database": "shop", "statement": "select id FROM orders"},
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

func TestStatementsPairWithoutTheirComments(t *testing.T) {
	session := exact.New().NewSession()
	statements := []string{}
	replay := func(_ context.Context, _ model.Route, key any) (normaliser.Reply, error) {
		statement, ok := key.(map[string]any)["statement"].(string)
		assert.True(t, ok)
		statements = append(statements, statement)
		return normaliser.OK{}, nil
	}

	for _, sql := range []string{
		"SELECT 1 -- app/controllers/health_controller.rb:35 speleo-trace-id=CJdPdYgHVQkZc",
		"SELECT 1 # trace\nFROM dual",
		"SELECT 2--1",
		"SELECT '-- kept', \"/* kept */\", `# kept`, 'it''s -- kept', 'a\\\\' -- dropped ",
		"SELECT /*! STRAIGHT_JOIN */ /*+ MAX_EXECUTION_TIME(1) */ id FROM t /* unclosed",
	} {
		_, err := session.Candidate(t.Context(), normaliser.Query{SQL: sql}, replay)
		assert.NoError(t, err)
	}
	assert.Equal(t, []string{
		"SELECT 1",
		"SELECT 1  \nFROM dual",
		"SELECT 2--1",
		"SELECT '-- kept', \"/* kept */\", `# kept`, 'it''s -- kept', 'a\\\\'",
		"SELECT /*! STRAIGHT_JOIN */ /*+ MAX_EXECUTION_TIME(1) */ id FROM t",
	}, statements)
}

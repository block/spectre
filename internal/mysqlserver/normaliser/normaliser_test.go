package normaliser_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"

	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

func TestStatementsParseIntoQueries(t *testing.T) {
	query, ok := normaliser.NewParser().Parse("SELECT 1; USE shop").(normaliser.Query)
	assert.True(t, ok)
	assert.Equal(t, "SELECT 1; USE shop", query.SQL)
	assert.Equal(t, []string{"SELECT 1", "USE `shop`"}, restored(t, query.Statements))
}

func TestStatementsThatDoNotParseAreUnparsed(t *testing.T) {
	for _, test := range []struct {
		name   string
		sql    string
		reason string
	}{
		{name: "Syntax", sql: "SELEKT 1", reason: "line 1 column 6 near \"SELEKT 1\" "},
		{name: "Empty", sql: " ", reason: "no statement"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, normaliser.Command(normaliser.UnparsedQuery{SQL: test.sql, Reason: test.reason}), normaliser.NewParser().Parse(test.sql))
		})
	}
}

func TestConcurrentStatementsKeepTheirOwnParts(t *testing.T) {
	parser := normaliser.NewParser()
	parsed := make([][]ast.StmtNode, 64)
	var wait sync.WaitGroup
	for index := range parsed {
		wait.Go(func() {
			query, ok := parser.Parse(fmt.Sprintf("SELECT %d", index)).(normaliser.Query)
			assert.True(t, ok)
			parsed[index] = query.Statements
		})
	}
	wait.Wait()
	for index, statements := range parsed {
		assert.Equal(t, []string{fmt.Sprintf("SELECT %d", index)}, restored(t, statements))
	}
}

// restored is each statement printed back from its parse tree.
func restored(t *testing.T, statements []ast.StmtNode) []string {
	t.Helper()
	texts := []string{}
	for _, statement := range statements {
		var text strings.Builder
		assert.NoError(t, statement.Restore(format.NewRestoreCtx(format.DefaultRestoreFlags, &text)))
		texts = append(texts, text.String())
	}
	return texts
}

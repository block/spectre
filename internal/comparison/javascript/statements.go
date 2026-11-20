package javascript

import (
	"fmt"
	"slices"
	"strings"

	. "github.com/alecthomas/types/optional"
)

// Row is one row of a statement's reply by column label, with NULL as None.
type Row map[string]Option[string]

// StatementReplacement declares that the candidate's statements replace the reference's
// statements. Exactly one side has more than one statement, or each side has one.
type StatementReplacement struct {
	index      int
	statements []string
	sources    []string
}

func newStatementReplacement(index int, statements, sources []string) StatementReplacement {
	return StatementReplacement{index: index, statements: slices.Clone(statements), sources: slices.Clone(sources)}
}

// Index returns the replacement's position among the scripts' statement replacements.
func (r StatementReplacement) Index() int {
	return r.index
}

// Statements returns the patterns of the candidate's statements.
func (r StatementReplacement) Statements() []string {
	return slices.Clone(r.statements)
}

// Sources returns the patterns of the reference's statements.
func (r StatementReplacement) Sources() []string {
	return slices.Clone(r.sources)
}

// Combines reports whether one candidate statement is answered by claiming the reference's
// statements. Otherwise one reference statement offers a reply to each candidate statement.
func (r StatementReplacement) Combines() bool {
	return len(r.statements) == 1
}

// String names the replacement the way a script declares it.
func (r StatementReplacement) String() string {
	quote := func(patterns []string) string {
		quoted := make([]string, 0, len(patterns))
		for _, pattern := range patterns {
			quoted = append(quoted, fmt.Sprintf("%q", pattern))
		}
		return strings.Join(quoted, ", ")
	}
	return fmt.Sprintf("egress.mysql.%s(%s).%s(%s).%s()", statementsMethod, quote(r.statements), replacesMethod, quote(r.sources), usingMethod)
}

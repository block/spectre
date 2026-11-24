package scripted

import (
	"fmt"
	"slices"
	"strings"
)

// The method names that a statement replacement is declared with.
const (
	statementMethod  = "statement"
	statementsMethod = "statements"
	replacesMethod   = "replaces"
	usingMethod      = "using"
)

// Replacement declares that the candidate's statements replace the reference's
// statements. One side has exactly one statement.
type Replacement struct {
	index      int
	statements []string
	sources    []string
	combines   bool
}

func newReplacement(index int, statements, sources []string, combines bool) Replacement {
	return Replacement{index: index, statements: slices.Clone(statements), sources: slices.Clone(sources), combines: combines}
}

// Index returns the replacement's position among the scripts' statement replacements.
func (r Replacement) Index() int {
	return r.index
}

// Statements returns the patterns of the candidate's statements.
func (r Replacement) Statements() []string {
	return slices.Clone(r.statements)
}

// Sources returns the patterns of the reference's statements.
func (r Replacement) Sources() []string {
	return slices.Clone(r.sources)
}

// Combines reports whether one candidate statement is answered by claiming the reference's
// statements. Otherwise one reference statement offers a reply to each candidate statement.
func (r Replacement) Combines() bool {
	return r.combines
}

// String names the replacement the way a script declares it.
func (r Replacement) String() string {
	quote := func(patterns []string) string {
		quoted := make([]string, 0, len(patterns))
		for _, pattern := range patterns {
			quoted = append(quoted, fmt.Sprintf("%q", pattern))
		}
		return strings.Join(quoted, ", ")
	}
	method := statementsMethod
	if r.combines {
		method = statementMethod
	}
	return fmt.Sprintf("egress.mysql.%s(%s).%s(%s).%s()", method, quote(r.statements), replacesMethod, quote(r.sources), usingMethod)
}

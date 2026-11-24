package scripted

import (
	_ "embed"

	"github.com/block/spectre/internal/comparison/javascript"
)

//go:embed members.d.ts
var membersDeclaration string

//go:embed payloads.d.ts
var payloadsDeclaration string

// Handle reads the statement replacements that scripts declared through the MySQL binding, and
// runs their callbacks. Only the MySQL server holds it, and its zero value reads nothing.
type Handle = javascript.Handle[Declarations, Callbacks]

// Declarations are what scripts declare through the MySQL binding.
type Declarations struct {
	// Replacements are the statement replacements in declaration order.
	Replacements []Replacement
}

// NewBinding returns the MySQL binding, which the script loader adds to its bindings, and the
// handle through which only the MySQL server reads it.
func NewBinding() (javascript.Binding, Handle) {
	return javascript.NewBinding(
		Protocol,
		membersDeclaration,
		payloadsDeclaration,
		map[string]javascript.TypeArguments{
			"EgressMySQL." + statementMethod:          javascript.TypeArgumentsCheckOnly,
			"EgressMySQL." + statementsMethod:         javascript.TypeArgumentsCheckOnly,
			"EgressMySQLStatement." + replacesMethod:  javascript.TypeArgumentsCheckOnly,
			"EgressMySQLStatements." + replacesMethod: javascript.TypeArgumentsCheckOnly,
		},
		define,
	)
}

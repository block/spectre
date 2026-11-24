package script

import (
	_ "embed"

	"github.com/block/spectre/internal/comparison/javascript"
)

//go:embed members.d.ts
var membersDeclaration string

//go:embed payloads.d.ts
var payloadsDeclaration string

// Handle reads the rules that scripts declared through the HTTP binding, and runs their callbacks.
// Only the HTTP server holds it, and its zero value reads nothing.
type Handle = javascript.Handle[Declarations, Callbacks]

// Declarations are what scripts declare through the HTTP binding.
type Declarations struct {
	// Rules are the ingress rules in declaration order.
	Rules []Rule
}

// New returns the HTTP binding, which the script loader adds to its bindings, and the
// handle through which only the HTTP server reads it.
func New() (javascript.Binding, Handle) {
	return javascript.NewBinding(
		Protocol,
		membersDeclaration,
		payloadsDeclaration,
		map[string]javascript.TypeArguments{
			"IngressHTTPRule." + matchMethod: javascript.TypeArgumentsBody,
		},
		define,
	)
}

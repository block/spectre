// Package script defines the event protocol's part of the spectre script module.
//
// Ingress rules say which reference event types start work and what the candidate receives;
// egress rules say which types work produces, what they pair with and what a send returns.
package script

import (
	_ "embed"

	"github.com/block/spectre/internal/comparison/javascript"
)

//go:embed members.d.ts
var membersDeclaration string

// Handle reads the rules that scripts declared through the event binding, and runs their
// callbacks. Only the event server holds it, and its zero value reads nothing.
type Handle = javascript.Handle[Declarations, Callbacks]

// NewBinding returns the event binding, which the script loader adds to its bindings, and
// the handle through which only the event server reads it.
func NewBinding() (javascript.Binding, Handle) {
	// The event protocol creates no payloads of its own, so it declares no payload modules.
	return javascript.NewBinding(Protocol, membersDeclaration, "", nil, define)
}

package javascript

import (
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/schema"
)

// Scope is what a binding builds its members with in one runtime. A binding's script
// functions run on the runtime's goroutine, so they may call a Scope without locking.
type Scope struct {
	runtime  *sobek.Runtime
	schema   *schema.Schema
	registry *callbackRegistry
}

func newScope(runtime *sobek.Runtime, loaded *schema.Schema, registry *callbackRegistry) *Scope {
	return &Scope{runtime: runtime, schema: loaded, registry: registry}
}

// Runtime returns the runtime that the binding defines its members in.
func (s *Scope) Runtime() *sobek.Runtime {
	return s.runtime
}

// Schema returns the payload types that the scripts were checked against.
func (s *Scope) Schema() *schema.Schema {
	return s.schema
}

// DeclareEndpoint records an endpoint that the engine pairs by. Called from a script
// function, it throws a TypeError for an endpoint the scripts already declared.
func (s *Scope) DeclareEndpoint(endpoint Endpoint) {
	s.registry.declareEndpoint(endpoint)
}

// DeclareReplacement records a replacement that the engine pairs by, with the script
// functions that map its requests and build its responses.
func (s *Scope) DeclareReplacement(replacement Replacement, request, response sobek.Callable) {
	s.registry.declareReplacement(replacement, replacementCallbacks{request: request, response: response})
}

// Call runs a script callback under the evaluator's timeout, converting values as JSON.
// An undefined result is None. It is available once the scripts have been evaluated.
func (s *Scope) Call(callback sobek.Callable, arguments ...schema.Value) (Option[schema.Value], error) {
	defer s.registry.count()()
	return s.registry.call(callback, arguments)
}

// Invoke calls a script callback directly with runtime values, under the evaluator's timeout. An
// exception becomes an error with message, without its text, but an interruption is kept.
func (s *Scope) Invoke(callback sobek.Callable, message string, arguments ...sobek.Value) (sobek.Value, error) {
	defer s.registry.count()()
	result, err := callback(sobek.Undefined(), arguments...)
	if err != nil {
		return nil, scriptError(err, message)
	}
	return result, nil
}

// Wait runs a function that a script callback waits on, such as for a reply from a database,
// without counting the time against the evaluator's timeout.
func (s *Scope) Wait(wait func() error) error {
	defer s.registry.exclude()()
	return wait()
}

// StringArgument reads a call's non-empty string argument, throwing a TypeError named
// spectre.<name> <argument> otherwise.
func (s *Scope) StringArgument(call sobek.FunctionCall, index int, name, argument string) string {
	return stringArgument(s.runtime, call, index, name, argument)
}

// TypeNameArgument reads a call's type name argument, as StringArgument reads a string.
func (s *Scope) TypeNameArgument(call sobek.FunctionCall, index int, name, argument string) schema.TypeRef {
	return schema.TypeRef(stringArgument(s.runtime, call, index, name, argument))
}

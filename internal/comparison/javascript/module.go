package javascript

import (
	"slices"
	"sort"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/grafana/sobek"
)

type targetKind string

const (
	targetField   targetKind = "field"
	targetMessage targetKind = "message"
)

// Direction names the proxy that decodes an endpoint's traffic.
type Direction string

const (
	// Ingress endpoints type responses from the proxied service. Patterns omit the host.
	Ingress Direction = "ingress"
	// Egress endpoints type requests to a dependency. Patterns include its host.
	Egress Direction = "egress"
)

// Endpoint maps a raw HTTP request pattern to the RPC method that types it.
type Endpoint struct {
	direction Direction
	pattern   string
	method    string
}

func newEndpoint(direction Direction, pattern, method string) Endpoint {
	return Endpoint{direction: direction, pattern: pattern, method: method}
}

// Direction returns the proxy that uses the endpoint.
func (e Endpoint) Direction() Direction {
	return e.direction
}

// Pattern returns the net/http.ServeMux pattern the endpoint matches.
func (e Endpoint) Pattern() string {
	return e.pattern
}

// Method returns the full name of the RPC method that types the endpoint.
func (e Endpoint) Method() string {
	return e.method
}

// registrations are the declarations one evaluation of a program produced.
type registrations struct {
	endpoints []Endpoint
	fields    []string
	messages  []string
}

// spectreModule is a stateless module record shared safely across isolated runtimes.
type spectreModule struct{}

var _ sobek.ModuleRecord = (*spectreModule)(nil)

func (m *spectreModule) Link() error {
	return nil
}

func (m *spectreModule) GetExportedNames(callback func([]string), _ ...sobek.ModuleRecord) bool {
	callback([]string{string(Ingress), string(Egress), string(targetField), string(targetMessage)})
	return true
}

func (m *spectreModule) ResolveExport(name string, _ ...sobek.ResolveSetElement) (*sobek.ResolvedBinding, bool) {
	kind := targetKind(name)
	direction := Direction(name)
	if direction != Ingress && direction != Egress && kind != targetField && kind != targetMessage {
		return nil, false
	}
	return &sobek.ResolvedBinding{Module: m, BindingName: name}, false
}

func (m *spectreModule) Evaluate(runtime *sobek.Runtime) *sobek.Promise {
	promise, resolve, _ := runtime.NewPromise()
	// Sobek re-evaluates non-cyclic modules at every import and keeps the last
	// instance, so reuse it or earlier importers' registrations would be lost.
	instance, ok := runtime.GetModuleInstance(m).(*callbackRegistry)
	if !ok {
		instance = newCallbackRegistry(runtime)
	}
	if err := resolve(instance); err != nil {
		panic(errors.Wrap(err, "resolve spectre module evaluation"))
	}
	return promise
}

// callbackRegistry is one runtime's module instance and owns all callable values.
// Evaluator access stays on the runtime's single caller, so it needs no locking.
type callbackRegistry struct {
	runtime   *sobek.Runtime
	functions map[string]sobek.Value
	endpoints []Endpoint
	fields    map[string]sobek.Callable
	messages  map[string]sobek.Callable
}

func newCallbackRegistry(runtime *sobek.Runtime) *callbackRegistry {
	registry := &callbackRegistry{
		runtime:  runtime,
		fields:   map[string]sobek.Callable{},
		messages: map[string]sobek.Callable{},
	}
	registry.functions = map[string]sobek.Value{
		string(Ingress):       runtime.ToValue(registry.endpointRegistration(Ingress)),
		string(Egress):        runtime.ToValue(registry.endpointRegistration(Egress)),
		string(targetField):   runtime.ToValue(registry.registration(targetField)),
		string(targetMessage): runtime.ToValue(registry.registration(targetMessage)),
	}
	return registry
}

func (r *callbackRegistry) endpointRegistration(direction Direction) func(sobek.FunctionCall) sobek.Value {
	return func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) != 2 {
			panic(r.runtime.NewTypeError("spectre.%s requires a pattern and an RPC method", direction))
		}
		pattern, ok := call.Argument(0).Export().(string)
		if !ok || pattern == "" {
			panic(r.runtime.NewTypeError("spectre.%s pattern must be a non-empty string", direction))
		}
		if direction == Ingress && !hasForm(pattern, false) {
			panic(r.runtime.NewTypeError("spectre.ingress pattern %q must have the form \"<METHOD> /<path>\"", pattern))
		}
		if direction == Egress && !hasForm(pattern, true) {
			panic(r.runtime.NewTypeError("spectre.egress pattern %q must have the form \"<METHOD> <host>/<path>\"", pattern))
		}
		method, ok := call.Argument(1).Export().(string)
		if !ok || method == "" {
			panic(r.runtime.NewTypeError("spectre.%s method must be a non-empty string", direction))
		}
		if slices.ContainsFunc(r.endpoints, func(endpoint Endpoint) bool { return endpoint.Pattern() == pattern }) {
			panic(r.runtime.NewTypeError("duplicate endpoint %q", pattern))
		}
		r.endpoints = append(r.endpoints, newEndpoint(direction, pattern, method))
		return sobek.Undefined()
	}
}

// hasForm reports whether a pattern names a method, and names a host exactly when
// host is set. Ingress receives requests for its own service, so it never routes by host.
func hasForm(pattern string, host bool) bool {
	fields := strings.Fields(pattern)
	return len(fields) == 2 && strings.HasPrefix(fields[1], "/") != host
}

func (r *callbackRegistry) GetBindingValue(name string) sobek.Value {
	return r.functions[name]
}

func (r *callbackRegistry) registration(kind targetKind) func(sobek.FunctionCall) sobek.Value {
	return func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) != 2 {
			panic(r.runtime.NewTypeError("spectre.%s requires exactly two arguments", kind))
		}
		target, ok := call.Argument(0).Export().(string)
		if !ok || target == "" {
			panic(r.runtime.NewTypeError("spectre.%s target must be a non-empty string", kind))
		}
		callback, ok := sobek.AssertFunction(call.Argument(1))
		if !ok {
			panic(r.runtime.NewTypeError("spectre.%s normaliser must be a function", kind))
		}
		if !r.register(kind, target, callback) {
			panic(r.runtime.NewTypeError("duplicate normaliser target %q", target))
		}
		return sobek.Undefined()
	}
}

func (r *callbackRegistry) register(kind targetKind, target string, callback sobek.Callable) bool {
	callbacks := r.callbacks(kind)
	if _, duplicate := callbacks[target]; duplicate {
		return false
	}
	callbacks[target] = callback
	return true
}

func (r *callbackRegistry) lookup(kind targetKind, target string) (sobek.Callable, bool) {
	callback, ok := r.callbacks(kind)[target]
	return callback, ok
}

func (r *callbackRegistry) registrations() registrations {
	return registrations{
		endpoints: slices.Clone(r.endpoints),
		fields:    r.targets(targetField),
		messages:  r.targets(targetMessage),
	}
}

func (r *callbackRegistry) targets(kind targetKind) []string {
	callbacks := r.callbacks(kind)
	targets := make([]string, 0, len(callbacks))
	for target := range callbacks {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}

func (r *callbackRegistry) callbacks(kind targetKind) map[string]sobek.Callable {
	switch kind {
	case targetField:
		return r.fields
	case targetMessage:
		return r.messages
	default:
		return nil
	}
}

// loadSpectreModule requires synchronous evaluation so registration is complete at return.
func loadSpectreModule(
	runtime *sobek.Runtime,
	entry *sobek.SourceTextModuleRecord,
	spectre *spectreModule,
) (*callbackRegistry, error) {
	promise := entry.Evaluate(runtime)
	switch promise.State() {
	case sobek.PromiseStateRejected:
		return nil, errors.Errorf("module evaluation rejected: %v", promise.Result())
	case sobek.PromiseStatePending:
		return nil, errors.New("module evaluation did not finish synchronously")
	case sobek.PromiseStateFulfilled:
	default:
		return nil, errors.Errorf("unknown module evaluation state %d", promise.State())
	}
	registry, ok := runtime.GetModuleInstance(spectre).(*callbackRegistry)
	if !ok {
		return nil, errors.New("spectre module instance is unavailable")
	}
	return registry, nil
}

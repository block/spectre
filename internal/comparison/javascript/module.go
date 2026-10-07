package javascript

import (
	"cmp"
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

// Endpoint maps a protocol-specific request pattern to a payload type.
type Endpoint struct {
	direction Direction
	protocol  string
	pattern   string
	typeName  string
}

func newEndpoint(direction Direction, protocol, pattern, typeName string) Endpoint {
	return Endpoint{direction: direction, protocol: protocol, pattern: pattern, typeName: typeName}
}

// Direction returns the proxy that uses the endpoint.
func (e Endpoint) Direction() Direction {
	return e.direction
}

// Protocol returns the protocol key that interprets the endpoint.
func (e Endpoint) Protocol() string {
	return e.protocol
}

// Pattern returns the protocol-specific pattern the endpoint matches.
func (e Endpoint) Pattern() string {
	return e.pattern
}

// Type returns the fully qualified name of the endpoint's payload type.
func (e Endpoint) Type() string {
	return e.typeName
}

// FieldTarget identifies a JSON field path relative to an explicitly named type.
// The two components remain separate because namespace names can overlap field paths.
type FieldTarget struct {
	typeName string
	path     string
}

// NewFieldTarget creates a target without resolving it against a schema.
func NewFieldTarget(typeName, path string) FieldTarget {
	return FieldTarget{typeName: typeName, path: path}
}

// Type returns the fully qualified name of the target's root object type.
func (t FieldTarget) Type() string {
	return t.typeName
}

// Path returns the JSON field path relative to the root type.
func (t FieldTarget) Path() string {
	return t.path
}

// String returns a human-readable target name, not a unique identity.
func (t FieldTarget) String() string {
	return t.typeName + "." + t.path
}

// registrations are the declarations one evaluation of a program produced.
type registrations struct {
	endpoints []Endpoint
	fields    []FieldTarget
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

// callbackRegistry is one runtime's module instance and owns the module's exported
// values. Evaluator access stays on the runtime's single caller, so it needs no locking.
type callbackRegistry struct {
	runtime   *sobek.Runtime
	functions map[string]sobek.Value
	endpoints []Endpoint
	fields    map[FieldTarget]sobek.Callable
	messages  map[string]sobek.Callable
}

func newCallbackRegistry(runtime *sobek.Runtime) *callbackRegistry {
	registry := &callbackRegistry{
		runtime:  runtime,
		fields:   map[FieldTarget]sobek.Callable{},
		messages: map[string]sobek.Callable{},
	}
	registry.functions = map[string]sobek.Value{
		string(Ingress):       registry.directionObject(Ingress),
		string(Egress):        registry.directionObject(Egress),
		string(targetField):   runtime.ToValue(registry.registration(targetField)),
		string(targetMessage): runtime.ToValue(registry.registration(targetMessage)),
	}
	return registry
}

// matchMethod names the method on the ingress and egress namespace objects that
// registers an endpoint for that direction.
const matchMethod = "match"

// directionObject builds the ingress or egress namespace object; its match method
// registers an endpoint for that direction.
func (r *callbackRegistry) directionObject(direction Direction) sobek.Value {
	object := r.runtime.NewObject()
	if err := object.Set(matchMethod, r.endpointRegistration(direction)); err != nil {
		panic(r.runtime.NewTypeError("define spectre.%s.%s: %v", direction, matchMethod, err))
	}
	return object
}

// endpointRegistration receives the type name compile inserts before the script's arguments.
func (r *callbackRegistry) endpointRegistration(direction Direction) func(sobek.FunctionCall) sobek.Value {
	qualified := string(direction) + "." + matchMethod
	return func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) != 3 {
			panic(r.runtime.NewTypeError("spectre.%s requires a type name, a protocol, and a pattern", qualified))
		}
		typeName := r.stringArgument(call, 0, qualified, "type name")
		protocol := r.stringArgument(call, 1, qualified, "protocol")
		pattern := r.stringArgument(call, 2, qualified, "pattern")
		if protocol == "http" && direction == Ingress && !hasForm(pattern, false) {
			panic(r.runtime.NewTypeError("spectre.%s pattern %q must have the form \"<METHOD> /<path>\"", qualified, pattern))
		}
		if protocol == "http" && direction == Egress && !hasForm(pattern, true) {
			panic(r.runtime.NewTypeError("spectre.%s pattern %q must have the form \"<METHOD> <host>/<path>\"", qualified, pattern))
		}
		if slices.ContainsFunc(r.endpoints, func(endpoint Endpoint) bool {
			return endpoint.Direction() == direction && endpoint.Protocol() == protocol && endpoint.Pattern() == pattern
		}) {
			panic(r.runtime.NewTypeError("duplicate endpoint %q", pattern))
		}
		r.endpoints = append(r.endpoints, newEndpoint(direction, protocol, pattern, typeName))
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
		argumentCount := 2
		if kind == targetField {
			argumentCount = 3
		}
		if len(call.Arguments) != argumentCount {
			panic(r.runtime.NewTypeError("spectre.%s requires exactly %d arguments", kind, argumentCount))
		}
		typeName := r.stringArgument(call, 0, string(kind), "type name")
		callback, ok := sobek.AssertFunction(call.Argument(argumentCount - 1))
		if !ok {
			panic(r.runtime.NewTypeError("spectre.%s normaliser must be a function", kind))
		}
		if kind == targetField {
			path := r.stringArgument(call, 1, string(kind), "path")
			target := NewFieldTarget(typeName, path)
			if _, duplicate := r.fields[target]; duplicate {
				panic(r.runtime.NewTypeError("duplicate normaliser target %q", target.String()))
			}
			r.fields[target] = callback
			return sobek.Undefined()
		}
		if _, duplicate := r.messages[typeName]; duplicate {
			panic(r.runtime.NewTypeError("duplicate normaliser target %q", typeName))
		}
		r.messages[typeName] = callback
		return sobek.Undefined()
	}
}

func (r *callbackRegistry) stringArgument(call sobek.FunctionCall, index int, name, argument string) string {
	value, ok := call.Argument(index).Export().(string)
	if !ok || value == "" {
		panic(r.runtime.NewTypeError("spectre.%s %s must be a non-empty string", name, argument))
	}
	return value
}

func (r *callbackRegistry) field(target FieldTarget) (sobek.Callable, bool) {
	callback, ok := r.fields[target]
	return callback, ok
}

func (r *callbackRegistry) message(typeName string) (sobek.Callable, bool) {
	callback, ok := r.messages[typeName]
	return callback, ok
}

func (r *callbackRegistry) registrations() registrations {
	fields := make([]FieldTarget, 0, len(r.fields))
	for target := range r.fields {
		fields = append(fields, target)
	}
	slices.SortFunc(fields, func(left, right FieldTarget) int {
		if order := cmp.Compare(left.Type(), right.Type()); order != 0 {
			return order
		}
		return cmp.Compare(left.Path(), right.Path())
	})
	messages := make([]string, 0, len(r.messages))
	for typeName := range r.messages {
		messages = append(messages, typeName)
	}
	sort.Strings(messages)
	return registrations{endpoints: slices.Clone(r.endpoints), fields: fields, messages: messages}
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

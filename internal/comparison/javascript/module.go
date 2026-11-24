package javascript

import (
	"cmp"
	"maps"
	"slices"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

type targetKind string

const (
	targetField   targetKind = "field"
	targetMessage targetKind = "message"
)

// Endpoint maps a protocol-specific request pattern to a payload type.
type Endpoint struct {
	direction model.Direction
	protocol  model.Protocol
	pattern   string
	typeName  schema.TypeRef
	optional  bool
}

// NewEndpoint creates an endpoint for a binding to declare through Scope.DeclareEndpoint.
func NewEndpoint(direction model.Direction, protocol model.Protocol, pattern string, typeName schema.TypeRef, optional bool) Endpoint {
	return Endpoint{direction: direction, protocol: protocol, pattern: pattern, typeName: typeName, optional: optional}
}

// Direction returns the proxy that uses the endpoint.
func (e Endpoint) Direction() model.Direction {
	return e.direction
}

// Protocol returns the protocol key that interprets the endpoint.
func (e Endpoint) Protocol() model.Protocol {
	return e.protocol
}

// Pattern returns the protocol-specific pattern the endpoint matches.
func (e Endpoint) Pattern() string {
	return e.pattern
}

// Type returns the fully qualified name of the endpoint's payload type.
func (e Endpoint) Type() schema.TypeRef {
	return e.typeName
}

// Optional reports whether the candidate may leave out the reference's requests to the endpoint.
func (e Endpoint) Optional() bool {
	return e.optional
}

// FieldTarget identifies a JSON field path relative to an explicitly named type.
// The two components remain separate because namespace names can overlap field paths.
type FieldTarget struct {
	typeName schema.TypeRef
	path     string
}

// NewFieldTarget creates a target without resolving it against a schema.
func NewFieldTarget(typeName schema.TypeRef, path string) FieldTarget {
	return FieldTarget{typeName: typeName, path: path}
}

// Type returns the fully qualified name of the target's root object type.
func (t FieldTarget) Type() schema.TypeRef {
	return t.typeName
}

// Path returns the JSON field path relative to the root type.
func (t FieldTarget) Path() string {
	return t.path
}

// String returns a human-readable target name, not a unique identity.
func (t FieldTarget) String() string {
	return string(t.typeName) + "." + t.path
}

// MessageTarget is one spectre.message declaration: every value of an object type
// normalises to a normalised type, through a script normaliser if Normalises.
type MessageTarget struct {
	typeName   schema.TypeRef
	normalised schema.TypeRef
	normalises bool
}

// NewMessageTarget creates a declaration without resolving it against a schema.
func NewMessageTarget(typeName, normalised schema.TypeRef, normalises bool) MessageTarget {
	return MessageTarget{typeName: typeName, normalised: normalised, normalises: normalises}
}

// Type returns the fully qualified name of the declared object type.
func (t MessageTarget) Type() schema.TypeRef {
	return t.typeName
}

// Normalised returns the name of the type the object type's values normalise to.
func (t MessageTarget) Normalised() schema.TypeRef {
	return t.normalised
}

// Normalises reports whether a script normaliser runs for every value of the type.
func (t MessageTarget) Normalises() bool {
	return t.normalises
}

// registrations are the declarations one evaluation of a program produced.
type registrations struct {
	endpoints    []Endpoint
	replacements []Replacement
	fields       []FieldTarget
	messages     []MessageTarget
	// declared holds each binding's declarations, in the order of the bindings.
	declared []declaredSnapshot
}

// sameRegistrations reports whether two evaluations produced the same declarations.
func sameRegistrations(left, right registrations) bool {
	return slices.Equal(left.endpoints, right.endpoints) &&
		slices.Equal(left.replacements, right.replacements) &&
		slices.Equal(left.fields, right.fields) &&
		slices.Equal(left.messages, right.messages) &&
		slices.EqualFunc(left.declared, right.declared, func(a, b declaredSnapshot) bool { return a.sameAs(b) })
}

// spectreModule is an immutable module record shared safely across isolated runtimes.
// Every runtime sees the same variables, so re-evaluation reproduces its registrations.
type spectreModule struct {
	loaded    *schema.Schema
	variables map[string]string
	bindings  []Binding
}

var _ sobek.ModuleRecord = (*spectreModule)(nil)

func (m *spectreModule) Link() error {
	return nil
}

func (m *spectreModule) GetExportedNames(callback func([]string), _ ...sobek.ModuleRecord) bool {
	callback([]string{string(model.Ingress), string(model.Egress), string(targetField), string(targetMessage), variableFunction})
	return true
}

func (m *spectreModule) ResolveExport(name string, _ ...sobek.ResolveSetElement) (*sobek.ResolvedBinding, bool) {
	kind := targetKind(name)
	direction := model.Direction(name)
	if direction != model.Ingress && direction != model.Egress && kind != targetField && kind != targetMessage && name != variableFunction {
		return nil, false
	}
	return &sobek.ResolvedBinding{Module: m, BindingName: name}, false
}

func (m *spectreModule) Evaluate(runtime *sobek.Runtime) *sobek.Promise {
	promise, resolve, reject := runtime.NewPromise()
	// Sobek re-evaluates non-cyclic modules at every import and keeps the last
	// instance, so reuse it or earlier importers' registrations would be lost.
	instance, ok := runtime.GetModuleInstance(m).(*callbackRegistry)
	if !ok {
		var err error
		instance, err = newCallbackRegistry(runtime, m.loaded, m.variables, m.bindings)
		if err != nil {
			if rejectErr := reject(err); rejectErr != nil {
				panic(errors.Wrap(rejectErr, "reject spectre module evaluation"))
			}
			return promise
		}
	}
	if err := resolve(instance); err != nil {
		panic(errors.Wrap(err, "resolve spectre module evaluation"))
	}
	return promise
}

// callbackRegistry is one runtime's module instance and owns the module's exported
// values. Evaluator access stays on the runtime's single caller, so it needs no locking.
type callbackRegistry struct {
	runtime *sobek.Runtime
	loaded  *schema.Schema
	// evaluator is set once the runtime has one, which is after the scripts are evaluated.
	evaluator Option[*Evaluator]
	functions map[string]sobek.Value
	// members holds each binding's members in the runtime, in the order of the bindings.
	members   []boundMembers
	endpoints []Endpoint
	// replacements and replacers are indexed alike.
	replacements []Replacement
	replacers    []replacementCallbacks
	fields       map[FieldTarget]sobek.Callable
	messages     map[schema.TypeRef]messageRegistration
}

// messageRegistration is one spectre.message call's normalised type and normaliser.
type messageRegistration struct {
	normalised schema.TypeRef
	callback   Option[sobek.Callable]
}

// replacementCallbacks are the script functions of one replacement.
type replacementCallbacks struct {
	request  sobek.Callable
	response sobek.Callable
}

// variableFunction reads a script variable and takes no type arguments.
const variableFunction = "variable"

// newCallbackRegistry defines the shared members, then each binding's.
func newCallbackRegistry(runtime *sobek.Runtime, loaded *schema.Schema, variables map[string]string, bindings []Binding) (*callbackRegistry, error) {
	registry := &callbackRegistry{
		runtime:  runtime,
		loaded:   loaded,
		fields:   map[FieldTarget]sobek.Callable{},
		messages: map[schema.TypeRef]messageRegistration{},
	}
	ingress := runtime.NewObject()
	egress := runtime.NewObject()
	for _, binding := range bindings {
		members, err := binding.defineIn(newScope(runtime, loaded, registry), ingress, egress)
		if err != nil {
			return nil, err
		}
		registry.members = append(registry.members, members)
	}
	registry.functions = map[string]sobek.Value{
		string(model.Ingress): ingress,
		string(model.Egress):  egress,
		string(targetField):   runtime.ToValue(registry.registerField),
		string(targetMessage): runtime.ToValue(registry.registerMessage),
		variableFunction:      runtime.ToValue(registry.variable(variables)),
	}
	return registry, nil
}

// attach gives the registry the evaluator that scripts' callbacks run under.
func (r *callbackRegistry) attach(evaluator *Evaluator) {
	r.evaluator = Some(evaluator)
}

// boundMembers returns each binding's members in the runtime, in the order of the bindings.
func (r *callbackRegistry) boundMembers() []boundMembers {
	return r.members
}

// declareEndpoint records an endpoint, throwing a TypeError if the scripts already declared it.
func (r *callbackRegistry) declareEndpoint(endpoint Endpoint) {
	if slices.ContainsFunc(r.endpoints, func(declared Endpoint) bool {
		return declared.Direction() == endpoint.Direction() && declared.Protocol() == endpoint.Protocol() && declared.Pattern() == endpoint.Pattern()
	}) {
		panic(r.runtime.NewTypeError("duplicate endpoint %q", endpoint.Pattern()))
	}
	r.endpoints = append(r.endpoints, endpoint)
}

func (r *callbackRegistry) declareReplacement(replacement Replacement, callbacks replacementCallbacks) {
	r.replacements = append(r.replacements, replacement.atIndex(len(r.replacements)))
	r.replacers = append(r.replacers, callbacks)
}

// call runs a script callback in the registry's evaluator, which exists once the scripts are evaluated.
func (r *callbackRegistry) call(callback sobek.Callable, arguments []schema.Value) (Option[schema.Value], error) {
	evaluator, ok := r.evaluator.Get()
	if !ok {
		return None[schema.Value](), errors.New("scripts are still being evaluated")
	}
	return evaluator.call(callback, arguments)
}

// count counts the time of a callback against the evaluator's timeout until the returned function
// is called. Callbacks cannot run while scripts are being evaluated, which has its own limit.
func (r *callbackRegistry) count() (stop func()) {
	evaluator, ok := r.evaluator.Get()
	if !ok {
		return func() {}
	}
	return evaluator.count()
}

// exclude stops counting the time of a callback against the evaluator's timeout, until the returned function is called.
func (r *callbackRegistry) exclude() (resume func()) {
	evaluator, ok := r.evaluator.Get()
	if !ok {
		return func() {}
	}
	return evaluator.exclude()
}

// variable returns the script function that reads a variable. An unset variable
// throws, so a script that needs it fails to load instead of using a wrong value.
func (r *callbackRegistry) variable(variables map[string]string) func(sobek.FunctionCall) sobek.Value {
	return func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) != 1 {
			panic(r.runtime.NewTypeError("spectre.%s requires a name", variableFunction))
		}
		name := r.stringArgument(call, 0, variableFunction, "name")
		value, ok := variables[name]
		if !ok {
			panic(r.runtime.NewTypeError("spectre.%s %q is not set", variableFunction, name))
		}
		return r.runtime.ToValue(value)
	}
}

func (r *callbackRegistry) GetBindingValue(name string) sobek.Value {
	return r.functions[name]
}

func (r *callbackRegistry) registerField(call sobek.FunctionCall) sobek.Value {
	if len(call.Arguments) != 3 {
		panic(r.runtime.NewTypeError("spectre.%s requires exactly 3 arguments", targetField))
	}
	typeName := schema.TypeRef(r.stringArgument(call, 0, string(targetField), "type name"))
	path := r.stringArgument(call, 1, string(targetField), "path")
	callback, ok := sobek.AssertFunction(call.Argument(2))
	if !ok {
		panic(r.runtime.NewTypeError("spectre.%s normaliser must be a function", targetField))
	}
	target := NewFieldTarget(typeName, path)
	if _, duplicate := r.fields[target]; duplicate {
		panic(r.runtime.NewTypeError("duplicate normaliser target %q", target.String()))
	}
	r.fields[target] = callback
	return sobek.Undefined()
}

// registerMessage takes the type name, the normalised type's name if the script
// gave one, and the normaliser if it gave one.
func (r *callbackRegistry) registerMessage(call sobek.FunctionCall) sobek.Value {
	kind := string(targetMessage)
	typeName := schema.TypeRef(r.stringArgument(call, 0, kind, "type name"))
	registration := messageRegistration{normalised: typeName}
	next := 1
	if _, named := call.Argument(next).Export().(string); named {
		registration.normalised = schema.TypeRef(r.stringArgument(call, next, kind, "normalised type name"))
		next++
	}
	if len(call.Arguments) > next {
		callback, ok := sobek.AssertFunction(call.Argument(next))
		if !ok || len(call.Arguments) > next+1 {
			panic(r.runtime.NewTypeError("spectre.%s takes at most one normaliser function", kind))
		}
		registration.callback = Some(callback)
	}
	if _, duplicate := r.messages[typeName]; duplicate {
		panic(r.runtime.NewTypeError("duplicate normaliser target %q", typeName))
	}
	r.messages[typeName] = registration
	return sobek.Undefined()
}

func (r *callbackRegistry) stringArgument(call sobek.FunctionCall, index int, name, argument string) string {
	return stringArgument(r.runtime, call, index, name, argument)
}

// stringArgument reads a non-empty string argument, throwing a TypeError otherwise.
func stringArgument(runtime *sobek.Runtime, call sobek.FunctionCall, index int, name, argument string) string {
	value, ok := call.Argument(index).Export().(string)
	if !ok || value == "" {
		panic(runtime.NewTypeError("spectre.%s %s must be a non-empty string", name, argument))
	}
	return value
}

func (r *callbackRegistry) field(target FieldTarget) (sobek.Callable, bool) {
	callback, ok := r.fields[target]
	return callback, ok
}

func (r *callbackRegistry) message(typeName schema.TypeRef) (sobek.Callable, bool) {
	return r.messages[typeName].callback.Get()
}

// replacer returns the callbacks of the replacement at index.
func (r *callbackRegistry) replacer(index int) (replacementCallbacks, bool) {
	if index < 0 || index >= len(r.replacers) {
		return replacementCallbacks{}, false
	}
	return r.replacers[index], true
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
	messages := make([]MessageTarget, 0, len(r.messages))
	for _, typeName := range slices.Sorted(maps.Keys(r.messages)) {
		registration := r.messages[typeName]
		messages = append(messages, NewMessageTarget(typeName, registration.normalised, registration.callback.Ok()))
	}
	declared := make([]declaredSnapshot, 0, len(r.members))
	for _, member := range r.members {
		declared = append(declared, member.snapshot())
	}
	return registrations{
		endpoints: slices.Clone(r.endpoints), replacements: slices.Clone(r.replacements),
		fields: fields, messages: messages, declared: declared,
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

package javascript

import (
	"cmp"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"
	"golang.org/x/net/http/httpguts"

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
}

func newEndpoint(direction model.Direction, protocol model.Protocol, pattern string, typeName schema.TypeRef) Endpoint {
	return Endpoint{direction: direction, protocol: protocol, pattern: pattern, typeName: typeName}
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

// registrations are the declarations one evaluation of a program produced.
type registrations struct {
	endpoints []Endpoint
	rules     []Rule
	fields    []FieldTarget
	messages  []schema.TypeRef
}

// spectreModule is a stateless module record shared safely across isolated runtimes.
type spectreModule struct{}

var _ sobek.ModuleRecord = (*spectreModule)(nil)

func (m *spectreModule) Link() error {
	return nil
}

func (m *spectreModule) GetExportedNames(callback func([]string), _ ...sobek.ModuleRecord) bool {
	callback([]string{string(model.Ingress), string(model.Egress), string(targetField), string(targetMessage)})
	return true
}

func (m *spectreModule) ResolveExport(name string, _ ...sobek.ResolveSetElement) (*sobek.ResolvedBinding, bool) {
	kind := targetKind(name)
	direction := model.Direction(name)
	if direction != model.Ingress && direction != model.Egress && kind != targetField && kind != targetMessage {
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
	rules     []Rule
	callbacks []ruleCallbacks
	fields    map[FieldTarget]sobek.Callable
	messages  map[schema.TypeRef]sobek.Callable
}

// ruleCallbacks are the script functions of one rule, indexed like the rules.
type ruleCallbacks struct {
	request Option[sobek.Callable]
	status  Option[sobek.Callable]
	// requestHeaders and responseHeaders hold header functions by canonical name.
	requestHeaders  map[string]sobek.Callable
	responseHeaders map[string]sobek.Callable
	normalise       Option[sobek.Callable]
}

func newCallbackRegistry(runtime *sobek.Runtime) *callbackRegistry {
	registry := &callbackRegistry{
		runtime:  runtime,
		fields:   map[FieldTarget]sobek.Callable{},
		messages: map[schema.TypeRef]sobek.Callable{},
	}
	ingress := runtime.NewObject()
	registry.defineMethod(ingress, "ingress."+string(model.ProtocolHTTP), string(model.ProtocolHTTP), registry.httpRule)
	egress := runtime.NewObject()
	registry.defineMethod(egress, "egress."+string(model.ProtocolHTTP), string(model.ProtocolHTTP), registry.egressRule)
	registry.functions = map[string]sobek.Value{
		string(model.Ingress): ingress,
		string(model.Egress):  egress,
		string(targetField):   runtime.ToValue(registry.registration(targetField)),
		string(targetMessage): runtime.ToValue(registry.registration(targetMessage)),
	}
	return registry
}

// matchMethod, ignoreMethod, statusMethod, and the header methods name the script API's methods
// whose type arguments, or lack of them, compile treats specially.
const (
	matchMethod  = "match"
	ignoreMethod = "ignore"
	statusMethod = "status"
	// requestHeaderMethod and responseHeaderMethod select by a header's value.
	requestHeaderMethod  = "requestHeader"
	responseHeaderMethod = "responseHeader"
	// textTypeName is the type name compile inserts for the spectre Text type.
	textTypeName = "spectre.Text"
)

// defineMethod binds one object method, panicking if sobek rejects it.
func (r *callbackRegistry) defineMethod(object *sobek.Object, qualified, method string, fn func(sobek.FunctionCall) sobek.Value) {
	if err := object.Set(method, fn); err != nil {
		panic(r.runtime.NewTypeError("define spectre.%s: %v", qualified, err))
	}
}

// egressRule starts an egress rule from a dependency's request pattern.
func (r *callbackRegistry) egressRule(call sobek.FunctionCall) sobek.Value {
	const qualified = "egress.http"
	if len(call.Arguments) != 1 {
		panic(r.runtime.NewTypeError("spectre.%s requires a pattern", qualified))
	}
	pattern := r.stringArgument(call, 0, qualified, "pattern")
	if !hasForm(pattern, true) {
		panic(r.runtime.NewTypeError("spectre.%s pattern %q must have the form \"<METHOD> <host>/<path>\"", qualified, pattern))
	}
	object := r.runtime.NewObject()
	r.defineMethod(object, "EgressHTTPRule."+matchMethod, matchMethod, func(call sobek.FunctionCall) sobek.Value {
		r.matchEndpoint(pattern, call)
		return sobek.Undefined()
	})
	return object
}

// matchEndpoint receives the type name compile inserts.
func (r *callbackRegistry) matchEndpoint(pattern string, call sobek.FunctionCall) {
	const qualified = "EgressHTTPRule.match"
	if len(call.Arguments) != 1 {
		panic(r.runtime.NewTypeError("spectre.%s requires a type name", qualified))
	}
	typeName := schema.TypeRef(r.stringArgument(call, 0, qualified, "type name"))
	if slices.ContainsFunc(r.endpoints, func(endpoint Endpoint) bool { return endpoint.Pattern() == pattern }) {
		panic(r.runtime.NewTypeError("duplicate endpoint %q", pattern))
	}
	r.endpoints = append(r.endpoints, newEndpoint(model.Egress, model.ProtocolHTTP, pattern, typeName))
}

// ruleBuilder is a rule whose selectors are chosen but whose action is not yet.
type ruleBuilder struct {
	protocol        model.Protocol
	request         RequestSelector
	status          StatusSelector
	requestHeaders  []HeaderSelector
	responseHeaders []HeaderSelector
	callbacks       ruleCallbacks
}

// httpRule starts an ingress rule from a pattern or a request function.
func (r *callbackRegistry) httpRule(call sobek.FunctionCall) sobek.Value {
	const qualified = "ingress.http"
	if len(call.Arguments) != 1 {
		panic(r.runtime.NewTypeError("spectre.%s requires a pattern or a function", qualified))
	}
	builder := ruleBuilder{protocol: model.ProtocolHTTP, status: StatusAny{}}
	if callback, ok := sobek.AssertFunction(call.Argument(0)); ok {
		builder.request = RequestScript{}
		builder.callbacks.request = Some(callback)
		return r.builderObject(builder)
	}
	pattern := r.stringArgument(call, 0, qualified, "pattern")
	if !hasForm(pattern, false) {
		panic(r.runtime.NewTypeError("spectre.%s pattern %q must have the form \"[<METHOD> ]/<path>\"", qualified, pattern))
	}
	builder.request = RequestPattern{Pattern: pattern}
	return r.builderObject(builder)
}

// builderObject exposes a builder to the script. Each method returns a new
// builder or registers a rule, so a builder can be shared between rules.
func (r *callbackRegistry) builderObject(builder ruleBuilder) sobek.Value {
	object := r.runtime.NewObject()
	r.defineMethod(object, "IngressHTTPRule."+statusMethod, statusMethod, func(call sobek.FunctionCall) sobek.Value {
		return r.builderObject(r.withStatus(builder, call))
	})
	for _, method := range []string{requestHeaderMethod, responseHeaderMethod} {
		r.defineMethod(object, "IngressHTTPRule."+method, method, func(call sobek.FunctionCall) sobek.Value {
			return r.builderObject(r.withHeader(builder, call, method))
		})
	}
	r.defineMethod(object, "IngressHTTPRule."+matchMethod, matchMethod, func(call sobek.FunctionCall) sobek.Value {
		r.matchRule(builder, call)
		return sobek.Undefined()
	})
	r.defineMethod(object, "IngressHTTPRule."+ignoreMethod, ignoreMethod, func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) != 0 {
			panic(r.runtime.NewTypeError("spectre.IngressHTTPRule.ignore takes no arguments"))
		}
		r.addRule(builder, ActionIgnore{})
		return sobek.Undefined()
	})
	return object
}

// withStatus narrows a builder to status codes, or to a status function.
func (r *callbackRegistry) withStatus(builder ruleBuilder, call sobek.FunctionCall) ruleBuilder {
	const qualified = "IngressHTTPRule.status"
	if _, unset := builder.status.(StatusAny); !unset {
		panic(r.runtime.NewTypeError("spectre.%s is already set for this rule", qualified))
	}
	if len(call.Arguments) == 0 {
		panic(r.runtime.NewTypeError("spectre.%s requires status codes or a function", qualified))
	}
	if callback, ok := sobek.AssertFunction(call.Argument(0)); ok {
		if len(call.Arguments) != 1 {
			panic(r.runtime.NewTypeError("spectre.%s takes either status codes or one function", qualified))
		}
		builder.status = StatusScript{}
		builder.callbacks.status = Some(callback)
		return builder
	}
	codes := make([]int, 0, len(call.Arguments))
	for _, argument := range call.Arguments {
		code, ok := argument.Export().(int64)
		if !ok || code < 100 || code > 599 {
			panic(r.runtime.NewTypeError("spectre.%s code %v must be an integer from 100 to 599", qualified, argument))
		}
		if slices.Contains(codes, int(code)) {
			panic(r.runtime.NewTypeError("spectre.%s code %d is repeated", qualified, code))
		}
		codes = append(codes, int(code))
	}
	builder.status = StatusCodes{Codes: codes}
	return builder
}

// withHeader narrows a builder by a request or response header's value, or by a
// function of it. method names the builder method that was called.
func (r *callbackRegistry) withHeader(builder ruleBuilder, call sobek.FunctionCall, method string) ruleBuilder {
	qualified := "IngressHTTPRule." + method
	selectors, callbacks := &builder.responseHeaders, &builder.callbacks.responseHeaders
	if method == requestHeaderMethod {
		selectors, callbacks = &builder.requestHeaders, &builder.callbacks.requestHeaders
	}
	if len(call.Arguments) != 2 {
		panic(r.runtime.NewTypeError("spectre.%s requires a header name and a value or a function", qualified))
	}
	name := r.stringArgument(call, 0, qualified, "header name")
	if !httpguts.ValidHeaderFieldName(name) {
		panic(r.runtime.NewTypeError("spectre.%s header name %q is not valid", qualified, name))
	}
	name = http.CanonicalHeaderKey(name)
	if slices.ContainsFunc(*selectors, func(header HeaderSelector) bool { return header.headerName() == name }) {
		panic(r.runtime.NewTypeError("spectre.%s header %q is already set for this rule", qualified, name))
	}
	headers := slices.Clone(*selectors)
	if callback, ok := sobek.AssertFunction(call.Argument(1)); ok {
		headers = append(headers, HeaderScript{Name: name})
		functions := map[string]sobek.Callable{name: callback}
		maps.Copy(functions, *callbacks)
		*callbacks = functions
	} else {
		value, isString := call.Argument(1).Export().(string)
		if !isString {
			panic(r.runtime.NewTypeError("spectre.%s value must be a string or a function", qualified))
		}
		headers = append(headers, HeaderValue{Name: name, Value: value})
	}
	slices.SortFunc(headers, func(left, right HeaderSelector) int {
		return cmp.Compare(left.headerName(), right.headerName())
	})
	*selectors = headers
	return builder
}

// matchRule receives the type name compile inserts before an optional normaliser.
func (r *callbackRegistry) matchRule(builder ruleBuilder, call sobek.FunctionCall) {
	const qualified = "IngressHTTPRule.match"
	if len(call.Arguments) < 1 || len(call.Arguments) > 2 {
		panic(r.runtime.NewTypeError("spectre.%s requires a type name and an optional normaliser", qualified))
	}
	typeName := Some(schema.TypeRef(r.stringArgument(call, 0, qualified, "type name")))
	if name, _ := typeName.Get(); name == textTypeName {
		typeName = None[schema.TypeRef]()
	}
	action := ActionMatch{Type: typeName}
	if len(call.Arguments) == 2 && !sobek.IsUndefined(call.Argument(1)) {
		callback, ok := sobek.AssertFunction(call.Argument(1))
		if !ok {
			panic(r.runtime.NewTypeError("spectre.%s normaliser must be a function", qualified))
		}
		action.Normalises = true
		builder.callbacks.normalise = Some(callback)
	}
	r.addRule(builder, action)
}

// addRule appends a rule. A rule whose selectors repeat an earlier rule's
// pattern, codes, and header values could never apply, so it is rejected.
func (r *callbackRegistry) addRule(builder ruleBuilder, action Action) {
	rule := newRule(len(r.rules), builder.protocol, builder.request, builder.status, builder.requestHeaders, builder.responseHeaders, action)
	if slices.ContainsFunc(r.rules, rule.sameSelectors) {
		panic(r.runtime.NewTypeError("duplicate rule %s", rule))
	}
	r.rules = append(r.rules, rule)
	r.callbacks = append(r.callbacks, builder.callbacks)
}

// hasForm reports whether a pattern names a host exactly when host is set. Egress
// patterns name a method; ingress receives requests for its own service, so it never routes by host.
func hasForm(pattern string, host bool) bool {
	fields := strings.Fields(pattern)
	if !host && len(fields) == 1 {
		return strings.HasPrefix(fields[0], "/")
	}
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
		typeName := schema.TypeRef(r.stringArgument(call, 0, string(kind), "type name"))
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

func (r *callbackRegistry) message(typeName schema.TypeRef) (sobek.Callable, bool) {
	callback, ok := r.messages[typeName]
	return callback, ok
}

// rule returns the callbacks of the rule at index, or none for an unknown rule.
func (r *callbackRegistry) rule(index int) ruleCallbacks {
	if index < 0 || index >= len(r.callbacks) {
		return ruleCallbacks{}
	}
	return r.callbacks[index]
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
	messages := slices.Sorted(maps.Keys(r.messages))
	return registrations{endpoints: slices.Clone(r.endpoints), rules: slices.Clone(r.rules), fields: fields, messages: messages}
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

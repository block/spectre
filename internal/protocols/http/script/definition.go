package script

import (
	"cmp"
	"maps"
	"net/http"
	"slices"
	"strings"

	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"
	"golang.org/x/net/http/httpguts"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// The names of the script API's methods that the HTTP binding defines.
const (
	matchMethod  = "match"
	ignoreMethod = "ignore"
	statusMethod = "status"
	// optionalMethod lets the candidate leave out an egress endpoint's reference requests.
	optionalMethod = "optional"
	// requestHeaderMethod and responseHeaderMethod select by a header's value.
	requestHeaderMethod  = "requestHeader"
	responseHeaderMethod = "responseHeader"
	// replacesMethod and usingMethod declare a replacement; each takes two type arguments.
	replacesMethod = "replaces"
	usingMethod    = "using"
)

// definition defines the HTTP binding's members in one runtime.
type definition struct {
	scope     *javascript.Scope
	runtime   *sobek.Runtime
	declared  *Declarations
	callbacks *runtimeCallbacks
}

func define(scope *javascript.Scope) (javascript.Members[Declarations, Callbacks], error) {
	return newDefinition(scope).members(), nil
}

func newDefinition(scope *javascript.Scope) *definition {
	return &definition{
		scope: scope, runtime: scope.Runtime(),
		declared: &Declarations{}, callbacks: &runtimeCallbacks{scope: scope},
	}
}

// members are the script API of the binding, bound to this definition's runtime.
func (d *definition) members() javascript.Members[Declarations, Callbacks] {
	return javascript.Members[Declarations, Callbacks]{
		Ingress: Some(d.runtime.ToValue(d.httpRule)),
		Egress:  Some(d.runtime.ToValue(d.egressRule)),
		Declared: func() Declarations {
			return Declarations{Rules: slices.Clone(d.declared.Rules)}
		},
		Instance: newCallbacks(d.callbacks),
	}
}

// defineMethod binds one object method, panicking if sobek rejects it.
func (d *definition) defineMethod(object *sobek.Object, qualified, method string, fn func(sobek.FunctionCall) sobek.Value) {
	if err := object.Set(method, fn); err != nil {
		panic(d.runtime.NewTypeError("define spectre.%s: %v", qualified, err))
	}
}

// egressRule starts an egress rule from a dependency's request pattern.
func (d *definition) egressRule(call sobek.FunctionCall) sobek.Value {
	const qualified = "egress.http"
	if len(call.Arguments) != 1 {
		panic(d.runtime.NewTypeError("spectre.%s requires a pattern", qualified))
	}
	pattern := d.scope.StringArgument(call, 0, qualified, "pattern")
	if !hasForm(pattern, true) {
		panic(d.runtime.NewTypeError("spectre.%s pattern %q must have the form \"<METHOD> <host>/<path>\"", qualified, pattern))
	}
	return d.egressRuleObject(pattern, false)
}

// egressRuleObject is an egress rule for pattern whose endpoint is not yet declared.
func (d *definition) egressRuleObject(pattern string, optional bool) sobek.Value {
	object := d.runtime.NewObject()
	d.defineMethod(object, "EgressHTTPRule."+optionalMethod, optionalMethod, func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) != 0 {
			panic(d.runtime.NewTypeError("spectre.EgressHTTPRule.%s takes no arguments", optionalMethod))
		}
		return d.egressRuleObject(pattern, true)
	})
	d.defineMethod(object, "EgressHTTPRule."+matchMethod, matchMethod, func(call sobek.FunctionCall) sobek.Value {
		d.matchEndpoint(pattern, optional, call)
		return sobek.Undefined()
	})
	d.defineMethod(object, "EgressHTTPRule."+replacesMethod, replacesMethod, func(call sobek.FunctionCall) sobek.Value {
		return d.replacementObject(pattern, optional, call)
	})
	return object
}

// replacementObject receives the source type names compile inserts before the source pattern.
func (d *definition) replacementObject(pattern string, optional bool, call sobek.FunctionCall) sobek.Value {
	const qualified = "EgressHTTPRule." + replacesMethod
	if len(call.Arguments) != 3 {
		panic(d.runtime.NewTypeError("spectre.%s requires a source pattern", qualified))
	}
	sourceRequest := d.scope.TypeNameArgument(call, 0, qualified, "source request type name")
	sourceResponse := d.scope.TypeNameArgument(call, 1, qualified, "source response type name")
	source := d.scope.StringArgument(call, 2, qualified, "source pattern")
	object := d.runtime.NewObject()
	d.defineMethod(object, "EgressReplacement."+usingMethod, usingMethod, func(call sobek.FunctionCall) sobek.Value {
		d.replaceUsing(pattern, optional, source, sourceRequest, sourceResponse, call)
		return sobek.Undefined()
	})
	return object
}

// replaceUsing receives the request and response type names compile inserts
// before the two functions.
func (d *definition) replaceUsing(pattern string, optional bool, source string, sourceRequest, sourceResponse schema.TypeRef, call sobek.FunctionCall) {
	const qualified = "EgressReplacement." + usingMethod
	if len(call.Arguments) != 4 {
		panic(d.runtime.NewTypeError("spectre.%s requires a request function and a response function", qualified))
	}
	request := d.scope.TypeNameArgument(call, 0, qualified, "request type name")
	response := d.scope.TypeNameArgument(call, 1, qualified, "response type name")
	requestFunction, ok := sobek.AssertFunction(call.Argument(2))
	if !ok {
		panic(d.runtime.NewTypeError("spectre.%s request must be a function", qualified))
	}
	responseFunction, ok := sobek.AssertFunction(call.Argument(3))
	if !ok {
		panic(d.runtime.NewTypeError("spectre.%s response must be a function", qualified))
	}
	d.scope.DeclareEndpoint(javascript.NewEndpoint(model.Egress, Protocol, pattern, request, optional))
	d.scope.DeclareReplacement(javascript.NewReplacement(Protocol, pattern, response, source, sourceRequest, sourceResponse), requestFunction, responseFunction)
}

// matchEndpoint receives the type name compile inserts.
func (d *definition) matchEndpoint(pattern string, optional bool, call sobek.FunctionCall) {
	const qualified = "EgressHTTPRule.match"
	if len(call.Arguments) != 1 {
		panic(d.runtime.NewTypeError("spectre.%s requires a type name", qualified))
	}
	typeName := d.scope.TypeNameArgument(call, 0, qualified, "type name")
	d.scope.DeclareEndpoint(javascript.NewEndpoint(model.Egress, Protocol, pattern, typeName, optional))
}

// ruleBuilder is a rule whose selectors are chosen but whose action is not yet.
type ruleBuilder struct {
	request         RequestSelector
	status          StatusSelector
	requestHeaders  []HeaderSelector
	responseHeaders []HeaderSelector
	callbacks       ruleCallbacks
}

// httpRule starts an ingress rule from a pattern or a request function.
func (d *definition) httpRule(call sobek.FunctionCall) sobek.Value {
	const qualified = "ingress.http"
	if len(call.Arguments) != 1 {
		panic(d.runtime.NewTypeError("spectre.%s requires a pattern or a function", qualified))
	}
	builder := ruleBuilder{status: StatusAny{}}
	if callback, ok := sobek.AssertFunction(call.Argument(0)); ok {
		builder.request = RequestScript{}
		builder.callbacks.request = Some(callback)
		return d.builderObject(builder)
	}
	pattern := d.scope.StringArgument(call, 0, qualified, "pattern")
	if !hasForm(pattern, false) {
		panic(d.runtime.NewTypeError("spectre.%s pattern %q must have the form \"[<METHOD> ]/<path>\"", qualified, pattern))
	}
	builder.request = RequestPattern{Pattern: pattern}
	return d.builderObject(builder)
}

// builderObject exposes a builder to the script. Each method returns a new
// builder or registers a rule, so a builder can be shared between rules.
func (d *definition) builderObject(builder ruleBuilder) sobek.Value {
	object := d.runtime.NewObject()
	d.defineMethod(object, "IngressHTTPRule."+statusMethod, statusMethod, func(call sobek.FunctionCall) sobek.Value {
		return d.builderObject(d.withStatus(builder, call))
	})
	for _, method := range []string{requestHeaderMethod, responseHeaderMethod} {
		d.defineMethod(object, "IngressHTTPRule."+method, method, func(call sobek.FunctionCall) sobek.Value {
			return d.builderObject(d.withHeader(builder, call, method))
		})
	}
	d.defineMethod(object, "IngressHTTPRule."+matchMethod, matchMethod, func(call sobek.FunctionCall) sobek.Value {
		d.matchRule(builder, call)
		return sobek.Undefined()
	})
	d.defineMethod(object, "IngressHTTPRule."+ignoreMethod, ignoreMethod, func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) != 0 {
			panic(d.runtime.NewTypeError("spectre.IngressHTTPRule.ignore takes no arguments"))
		}
		d.addRule(builder, ActionIgnore{})
		return sobek.Undefined()
	})
	return object
}

// withStatus narrows a builder to status codes, or to a status function.
func (d *definition) withStatus(builder ruleBuilder, call sobek.FunctionCall) ruleBuilder {
	const qualified = "IngressHTTPRule.status"
	if _, unset := builder.status.(StatusAny); !unset {
		panic(d.runtime.NewTypeError("spectre.%s is already set for this rule", qualified))
	}
	if len(call.Arguments) == 0 {
		panic(d.runtime.NewTypeError("spectre.%s requires status codes or a function", qualified))
	}
	if callback, ok := sobek.AssertFunction(call.Argument(0)); ok {
		if len(call.Arguments) != 1 {
			panic(d.runtime.NewTypeError("spectre.%s takes either status codes or one function", qualified))
		}
		builder.status = StatusScript{}
		builder.callbacks.status = Some(callback)
		return builder
	}
	codes := make([]int, 0, len(call.Arguments))
	for _, argument := range call.Arguments {
		code, ok := argument.Export().(int64)
		if !ok || code < 100 || code > 599 {
			panic(d.runtime.NewTypeError("spectre.%s code %v must be an integer from 100 to 599", qualified, argument))
		}
		if slices.Contains(codes, int(code)) {
			panic(d.runtime.NewTypeError("spectre.%s code %d is repeated", qualified, code))
		}
		codes = append(codes, int(code))
	}
	builder.status = StatusCodes{Codes: codes}
	return builder
}

// withHeader narrows a builder by a request or response header's value, or by a
// function of it. method names the builder method that was called.
func (d *definition) withHeader(builder ruleBuilder, call sobek.FunctionCall, method string) ruleBuilder {
	qualified := "IngressHTTPRule." + method
	selectors, callbacks := &builder.responseHeaders, &builder.callbacks.responseHeaders
	if method == requestHeaderMethod {
		selectors, callbacks = &builder.requestHeaders, &builder.callbacks.requestHeaders
	}
	if len(call.Arguments) != 2 {
		panic(d.runtime.NewTypeError("spectre.%s requires a header name and a value or a function", qualified))
	}
	name := d.scope.StringArgument(call, 0, qualified, "header name")
	if !httpguts.ValidHeaderFieldName(name) {
		panic(d.runtime.NewTypeError("spectre.%s header name %q is not valid", qualified, name))
	}
	name = http.CanonicalHeaderKey(name)
	if slices.ContainsFunc(*selectors, func(header HeaderSelector) bool { return header.headerName() == name }) {
		panic(d.runtime.NewTypeError("spectre.%s header %q is already set for this rule", qualified, name))
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
			panic(d.runtime.NewTypeError("spectre.%s value must be a string or a function", qualified))
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
func (d *definition) matchRule(builder ruleBuilder, call sobek.FunctionCall) {
	const qualified = "IngressHTTPRule.match"
	if len(call.Arguments) < 1 || len(call.Arguments) > 2 {
		panic(d.runtime.NewTypeError("spectre.%s requires a type name and an optional normaliser", qualified))
	}
	action := ActionMatch{Type: d.bodyType(d.scope.TypeNameArgument(call, 0, qualified, "type name"))}
	if len(call.Arguments) == 2 && !sobek.IsUndefined(call.Argument(1)) {
		callback, ok := sobek.AssertFunction(call.Argument(1))
		if !ok {
			panic(d.runtime.NewTypeError("spectre.%s normaliser must be a function", qualified))
		}
		action.Normalises = true
		builder.callbacks.normalise = Some(callback)
	}
	d.addRule(builder, action)
}

// bodyType returns the type of the bodies a rule decodes as the type name compile inserted.
func (d *definition) bodyType(name schema.TypeRef) schema.Type {
	switch name {
	case javascript.TextTypeName:
		return schema.StringType{}
	case javascript.UnknownTypeName:
		return schema.UnknownType{}
	}
	declared, err := d.scope.Schema().Type(name)
	if err != nil {
		panic(d.runtime.NewTypeError("spectre.IngressHTTPRule.match: %v", err))
	}
	return declared
}

// addRule appends a rule. A rule whose selectors repeat an earlier rule's
// pattern, codes, and header values could never apply, so it is rejected.
func (d *definition) addRule(builder ruleBuilder, action Action) {
	rule := newRule(len(d.declared.Rules), builder.request, builder.status, builder.requestHeaders, builder.responseHeaders, action)
	if slices.ContainsFunc(d.declared.Rules, rule.sameSelectors) {
		panic(d.runtime.NewTypeError("duplicate rule %s", rule))
	}
	d.declared.Rules = append(d.declared.Rules, rule)
	d.callbacks.rules = append(d.callbacks.rules, builder.callbacks)
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

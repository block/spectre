// Package javascripttest provides a script binding of a made-up protocol, for testing the
// code that every protocol shares without depending on a real protocol.
package javascripttest

import (
	_ "embed"

	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
)

// Protocol is the protocol of the binding.
const Protocol model.Protocol = "fake"

//go:embed members.d.ts
var members string

// Binding returns a binding that adds egress.fake(pattern) to the script API. Its rules declare
// endpoints and replacements, which the engine pairs by, and have no other effect.
func Binding() javascript.Binding {
	binding, _ := javascript.NewBinding(
		Protocol,
		members,
		"",
		nil,
		func(scope *javascript.Scope) (javascript.Members[struct{}, struct{}], error) {
			egress := func(call sobek.FunctionCall) sobek.Value {
				return newRule(scope, scope.StringArgument(call, 0, "egress.fake", "pattern"), false)
			}
			return javascript.Members[struct{}, struct{}]{
				Egress:   Some(scope.Runtime().ToValue(egress)),
				Declared: func() struct{} { return struct{}{} },
			}, nil
		},
	)
	return binding
}

// newRule is an egress rule for pattern, whose endpoint is not yet declared.
func newRule(scope *javascript.Scope, pattern string, optional bool) sobek.Value {
	runtime := scope.Runtime()
	object := runtime.NewObject()
	set := func(name string, method func(call sobek.FunctionCall) sobek.Value) {
		if err := object.Set(name, method); err != nil {
			panic(runtime.NewTypeError("define %s: %v", name, err))
		}
	}
	set("optional", func(sobek.FunctionCall) sobek.Value {
		return newRule(scope, pattern, true)
	})
	set("match", func(call sobek.FunctionCall) sobek.Value {
		typeName := scope.TypeNameArgument(call, 0, "match", "type name")
		scope.DeclareEndpoint(javascript.NewEndpoint(model.Egress, Protocol, pattern, typeName, optional))
		return sobek.Undefined()
	})
	set("replaces", func(call sobek.FunctionCall) sobek.Value {
		return newReplacement(scope, pattern, optional, call)
	})
	return object
}

// newReplacement receives the source type names that compile inserts before the source pattern.
func newReplacement(scope *javascript.Scope, pattern string, optional bool, call sobek.FunctionCall) sobek.Value {
	sourceRequest := scope.TypeNameArgument(call, 0, "replaces", "source request type name")
	sourceResponse := scope.TypeNameArgument(call, 1, "replaces", "source response type name")
	source := scope.StringArgument(call, 2, "replaces", "source pattern")
	runtime := scope.Runtime()
	object := runtime.NewObject()
	err := object.Set("using", func(call sobek.FunctionCall) sobek.Value {
		request := scope.TypeNameArgument(call, 0, "using", "request type name")
		response := scope.TypeNameArgument(call, 1, "using", "response type name")
		requestFunction, requestOK := sobek.AssertFunction(call.Argument(2))
		responseFunction, responseOK := sobek.AssertFunction(call.Argument(3))
		if !requestOK || !responseOK {
			panic(runtime.NewTypeError("using requires a request function and a response function"))
		}
		scope.DeclareEndpoint(javascript.NewEndpoint(model.Egress, Protocol, pattern, request, optional))
		scope.DeclareReplacement(javascript.NewReplacement(Protocol, pattern, response, source, sourceRequest, sourceResponse), requestFunction, responseFunction)
		return sobek.Undefined()
	})
	if err != nil {
		panic(runtime.NewTypeError("define using: %v", err))
	}
	return object
}

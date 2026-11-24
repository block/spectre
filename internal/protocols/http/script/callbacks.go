package script

import (
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/schema"
)

// ruleCallbacks are the script functions of one rule, indexed like the rules.
type ruleCallbacks struct {
	request Option[sobek.Callable]
	status  Option[sobek.Callable]
	// requestHeaders and responseHeaders hold header functions by canonical name.
	requestHeaders  map[string]sobek.Callable
	responseHeaders map[string]sobek.Callable
	normalise       Option[sobek.Callable]
}

// runtimeCallbacks are one runtime's script functions of the declared rules, indexed alike.
type runtimeCallbacks struct {
	scope *javascript.Scope
	rules []ruleCallbacks
}

// Callbacks are the script functions of the rules in one evaluator's runtime, so they are
// confined to its caller. The zero value stands for an evaluator that was not built with the binding.
type Callbacks struct {
	runtime Option[*runtimeCallbacks]
}

func newCallbacks(runtime *runtimeCallbacks) Callbacks {
	return Callbacks{runtime: Some(runtime)}
}

// newUnavailableCallbacks stands for an evaluator that was not built with the binding.
func newUnavailableCallbacks() Callbacks {
	return Callbacks{}
}

// rule returns the callbacks of the rule at index and the scope that runs them. A rule
// that is unknown has no callbacks, and no scope.
func (c Callbacks) rule(index int) (ruleCallbacks, *javascript.Scope) {
	runtime, ok := c.runtime.Get()
	if !ok || index < 0 || index >= len(runtime.rules) {
		return ruleCallbacks{}, nil
	}
	return runtime.rules[index], runtime.scope
}

// SelectsRequest reports whether a rule's request function accepts a request.
func (c Callbacks) SelectsRequest(rule Rule, request Request) (bool, error) {
	callbacks, scope := c.rule(rule.Index())
	callback, ok := callbacks.request.Get()
	if !ok {
		return false, errors.Errorf("JavaScript request function of rule %s is unavailable", rule)
	}
	runtime := scope.Runtime()
	headers := runtime.NewObject()
	for name := range request.Header {
		value, _ := request.HeaderValue(name).Get()
		if err := headers.Set(strings.ToLower(name), value); err != nil {
			return false, errors.Wrap(err, "build request headers argument")
		}
	}
	argument := runtime.NewObject()
	for name, value := range map[string]sobek.Value{"method": runtime.ToValue(request.Method), "path": runtime.ToValue(request.Path), "headers": headers} {
		if err := argument.Set(name, value); err != nil {
			return false, errors.Wrap(err, "build request argument")
		}
	}
	return callSelector(scope, rule, callback, argument)
}

// SelectsStatus reports whether a rule's status function accepts a status.
func (c Callbacks) SelectsStatus(rule Rule, status int) (bool, error) {
	callbacks, scope := c.rule(rule.Index())
	callback, ok := callbacks.status.Get()
	if !ok {
		return false, errors.Errorf("JavaScript status function of rule %s is unavailable", rule)
	}
	return callSelector(scope, rule, callback, scope.Runtime().ToValue(status))
}

// SelectsRequestHeader reports whether a rule's function for a request header
// accepts its value, which is undefined when the request lacks the header.
func (c Callbacks) SelectsRequestHeader(rule Rule, name string, value Option[string]) (bool, error) {
	callbacks, scope := c.rule(rule.Index())
	return selectsHeader(scope, rule, callbacks.requestHeaders, "request", name, value)
}

// SelectsResponseHeader reports whether a rule's function for a response header
// accepts its value, which is undefined when the response lacks the header.
func (c Callbacks) SelectsResponseHeader(rule Rule, name string, value Option[string]) (bool, error) {
	callbacks, scope := c.rule(rule.Index())
	return selectsHeader(scope, rule, callbacks.responseHeaders, "response", name, value)
}

// NormaliseResponse invokes a rule's response normaliser on a JSON response
// object. None stands for an undefined result.
func (c Callbacks) NormaliseResponse(rule Rule, response schema.Value) (Option[schema.Value], error) {
	callbacks, scope := c.rule(rule.Index())
	callback, ok := callbacks.normalise.Get()
	if !ok {
		return None[schema.Value](), errors.Errorf("JavaScript normaliser of rule %s is unavailable", rule)
	}
	return errors.WithStack2(scope.Call(callback, response))
}

func selectsHeader(scope *javascript.Scope, rule Rule, callbacks map[string]sobek.Callable, message, name string, value Option[string]) (bool, error) {
	callback, ok := callbacks[name]
	if !ok {
		return false, errors.Errorf("JavaScript function for %s header %q of rule %s is unavailable", message, name, rule)
	}
	argument := sobek.Undefined()
	if present, found := value.Get(); found {
		argument = scope.Runtime().ToValue(present)
	}
	return callSelector(scope, rule, callback, argument)
}

func callSelector(scope *javascript.Scope, rule Rule, callback sobek.Callable, argument sobek.Value) (bool, error) {
	read := func(result sobek.Value) (bool, error) {
		selected, ok := result.Export().(bool)
		if !ok {
			return false, errors.Errorf("JavaScript selector of rule %s returned a value that is not a boolean", rule)
		}
		return selected, nil
	}
	selected, err := scope.Invoke(callback, "JavaScript selector of rule "+rule.String()+" threw an exception", read, argument)
	return selected, errors.WithStack(err)
}

package javascript

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// Evaluator owns the JavaScript runtime for one payload normalisation.
// It is confined to its caller because Sobek runtime access is not synchronized.
type Evaluator struct {
	runtime   *sobek.Runtime
	parse     sobek.Callable
	stringify sobek.Callable
	registry  *callbackRegistry
	// stop is None once Close has released the context watcher.
	stop Option[func()]
}

// JSON helpers are captured before the script runs so it cannot replace them. The
// replacer rejects values JSON would silently drop or coerce.
const jsonHelpers = `(function(parse, stringify, isArray, isFinite) {
	const replacer = function(key, value) {
		if (value === undefined && !isArray(this)) {
			return value;
		}
		if (value === undefined || typeof value === "function" || typeof value === "symbol" ||
			typeof value === "bigint" || (typeof value === "number" && !isFinite(value))) {
			throw new TypeError("normalised value is not representable as JSON");
		}
		return value;
	};
	return {
		parse: function(text) { return parse(text); },
		stringify: function(value) { return stringify(value, replacer); },
	};
})(JSON.parse, JSON.stringify, Array.isArray, Number.isFinite)`

// newEvaluator builds a runtime from the exact compiled values owned by Program.
func newEvaluator(
	ctx context.Context,
	entry *sobek.SourceTextModuleRecord,
	spectre *spectreModule,
) (*Evaluator, registrations, error) {
	runtime := sobek.New()
	runtime.SetMaxCallStackSize(1024)
	evaluator := &Evaluator{runtime: runtime, stop: Some(watchRuntime(ctx, runtime))}
	fail := func(err error) (*Evaluator, registrations, error) {
		evaluator.Close()
		// Module evaluation can hide cancellation, so preserve it alongside the setup error.
		if ctx.Err() != nil {
			return nil, registrations{}, errors.Join(err, ctx.Err())
		}
		return nil, registrations{}, err
	}
	helpers, err := runtime.RunString(jsonHelpers)
	if err != nil {
		return fail(errors.Wrap(err, "create JavaScript JSON helpers"))
	}
	helperObject := helpers.ToObject(runtime)
	evaluator.parse, _ = sobek.AssertFunction(helperObject.Get("parse"))
	evaluator.stringify, _ = sobek.AssertFunction(helperObject.Get("stringify"))
	if evaluator.parse == nil || evaluator.stringify == nil {
		evaluator.Close()
		return nil, registrations{}, errors.New("JavaScript JSON helpers are not callable")
	}
	registry, err := loadSpectreModule(runtime, entry, spectre)
	if err != nil {
		return fail(err)
	}
	evaluator.registry = registry
	return evaluator, registry.registrations(), nil
}

// Close releases the evaluator's context watcher.
func (e *Evaluator) Close() {
	stop, ok := e.stop.Get()
	if !ok {
		return
	}
	stop()
	e.stop = None[func()]()
}

// NormaliseField invokes the normaliser registered for a JSON field path. None
// stands for an absent field in both the argument and the result.
func (e *Evaluator) NormaliseField(target FieldTarget, value Option[model.DynamicValue]) (Option[model.DynamicValue], error) {
	callback, ok := e.registry.field(target)
	if !ok {
		return None[model.DynamicValue](), errors.Errorf("JavaScript field normaliser (%q, %q) is unavailable", target.Type(), target.Path())
	}
	return e.normalise(callback, value)
}

// NormaliseMessage invokes the normaliser registered for an object type. None
// stands for an absent message in both the argument and the result.
func (e *Evaluator) NormaliseMessage(target schema.TypeRef, value Option[model.DynamicValue]) (Option[model.DynamicValue], error) {
	callback, ok := e.registry.message(target)
	if !ok {
		return None[model.DynamicValue](), errors.Errorf("JavaScript message normaliser %q is unavailable", target)
	}
	return e.normalise(callback, value)
}

// SelectsRequest reports whether a rule's request function accepts a request.
func (e *Evaluator) SelectsRequest(rule Rule, request Request) (bool, error) {
	callbacks := e.registry.rule(rule.Index())
	callback, ok := callbacks.request.Get()
	if !ok {
		return false, errors.Errorf("JavaScript request function of rule %s is unavailable", rule)
	}
	headers := e.runtime.NewObject()
	for name := range request.Header {
		value, _ := request.HeaderValue(name).Get()
		if err := headers.Set(strings.ToLower(name), value); err != nil {
			return false, errors.Wrap(err, "build request headers argument")
		}
	}
	argument := e.runtime.NewObject()
	for name, value := range map[string]sobek.Value{"method": e.runtime.ToValue(request.Method), "path": e.runtime.ToValue(request.Path), "headers": headers} {
		if err := argument.Set(name, value); err != nil {
			return false, errors.Wrap(err, "build request argument")
		}
	}
	return e.selects(rule, callback, argument)
}

// SelectsStatus reports whether a rule's status function accepts a status.
func (e *Evaluator) SelectsStatus(rule Rule, status int) (bool, error) {
	callbacks := e.registry.rule(rule.Index())
	callback, ok := callbacks.status.Get()
	if !ok {
		return false, errors.Errorf("JavaScript status function of rule %s is unavailable", rule)
	}
	return e.selects(rule, callback, e.runtime.ToValue(status))
}

// SelectsRequestHeader reports whether a rule's function for a request header
// accepts its value, which is undefined when the request lacks the header.
func (e *Evaluator) SelectsRequestHeader(rule Rule, name string, value Option[string]) (bool, error) {
	return e.selectsHeader(rule, e.registry.rule(rule.Index()).requestHeaders, "request", name, value)
}

// SelectsResponseHeader reports whether a rule's function for a response header
// accepts its value, which is undefined when the response lacks the header.
func (e *Evaluator) SelectsResponseHeader(rule Rule, name string, value Option[string]) (bool, error) {
	return e.selectsHeader(rule, e.registry.rule(rule.Index()).responseHeaders, "response", name, value)
}

func (e *Evaluator) selectsHeader(rule Rule, callbacks map[string]sobek.Callable, message, name string, value Option[string]) (bool, error) {
	callback, ok := callbacks[name]
	if !ok {
		return false, errors.Errorf("JavaScript function for %s header %q of rule %s is unavailable", message, name, rule)
	}
	argument := sobek.Undefined()
	if present, found := value.Get(); found {
		argument = e.runtime.ToValue(present)
	}
	return e.selects(rule, callback, argument)
}

func (e *Evaluator) selects(rule Rule, callback sobek.Callable, argument sobek.Value) (bool, error) {
	result, err := callback(sobek.Undefined(), argument)
	if err != nil {
		return false, scriptError(err, "JavaScript selector of rule "+rule.String()+" threw an exception")
	}
	selected, ok := result.Export().(bool)
	if !ok {
		return false, errors.Errorf("JavaScript selector of rule %s returned a value that is not a boolean", rule)
	}
	return selected, nil
}

// NormaliseResponse invokes a rule's response normaliser on a JSON response
// object. None stands for an undefined result.
func (e *Evaluator) NormaliseResponse(rule Rule, response model.DynamicValue) (Option[model.DynamicValue], error) {
	callbacks := e.registry.rule(rule.Index())
	callback, ok := callbacks.normalise.Get()
	if !ok {
		return None[model.DynamicValue](), errors.Errorf("JavaScript normaliser of rule %s is unavailable", rule)
	}
	return e.normalise(callback, Some(response))
}

// ReplaceRequest maps a request to a replacing endpoint to the source request it pairs with.
func (e *Evaluator) ReplaceRequest(replacement Replacement, request model.DynamicValue) (model.DynamicValue, error) {
	callbacks, ok := e.registry.replacer(replacement.Index())
	if !ok {
		return nil, errors.Errorf("JavaScript request function of %s is unavailable", replacement)
	}
	return e.replace(replacement, "request", callbacks.request, request)
}

// ReplaceResponse builds the answer to a request to a replacing endpoint from a
// reference response to its source.
func (e *Evaluator) ReplaceResponse(replacement Replacement, request, reference model.DynamicValue) (model.DynamicValue, error) {
	callbacks, ok := e.registry.replacer(replacement.Index())
	if !ok {
		return nil, errors.Errorf("JavaScript response function of %s is unavailable", replacement)
	}
	return e.replace(replacement, "response", callbacks.response, request, reference)
}

func (e *Evaluator) replace(replacement Replacement, function string, callback sobek.Callable, values ...model.DynamicValue) (model.DynamicValue, error) {
	arguments := make([]sobek.Value, 0, len(values))
	for _, value := range values {
		argument, err := e.argument(Some(value))
		if err != nil {
			return nil, errors.Wrapf(err, "clone %s argument", function)
		}
		arguments = append(arguments, argument)
	}
	result, err := callback(sobek.Undefined(), arguments...)
	if err != nil {
		return nil, errors.Errorf("JavaScript %s function of %s threw an exception", function, replacement)
	}
	if sobek.IsUndefined(result) {
		return nil, errors.Errorf("JavaScript %s function of %s returned undefined", function, replacement)
	}
	value, err := e.result(result)
	return value, errors.Wrapf(err, "%s function of %s", function, replacement)
}

// Claim returns the rows of the reference statement with values that a combining
// replacement's using function claims from one of its sources.
type Claim func(values []Option[string]) ([]Row, error)

// Offer receives each reply that a splitting replacement's using function offers for
// the candidate statement at index, with its values.
type Offer func(index int, values []Option[string], rows []Row) error

// CombineStatements answers a candidate statement with the rows a combining replacement's using
// function builds from claims, one per source. Only time outside claims counts against limit.
func (e *Evaluator) CombineStatements(limit time.Duration, replacement StatementReplacement, values []Option[string], claims []Claim) ([]Row, error) {
	using, ok := e.registry.statementUser(replacement.Index())
	if !ok {
		return nil, errors.Errorf("JavaScript function of %s is unavailable", replacement)
	}
	clock := startStopwatch(e.runtime, limit)
	defer clock.stop()
	failure := None[error]()
	functions := make([]sobek.Value, 0, len(claims))
	for _, claim := range claims {
		functions = append(functions, e.runtime.ToValue(func(call sobek.FunctionCall) sobek.Value {
			claimed, err := e.statementValues(call.Arguments)
			var rows []Row
			if err == nil {
				clock.pause()
				rows, err = claim(claimed)
				clock.resume()
			}
			if err != nil {
				failure = Some(err)
				panic(e.runtime.NewGoError(err))
			}
			return e.rowsValue(rows)
		}))
	}
	result, err := using(sobek.Undefined(), e.valuesValue(values), e.array(functions))
	if failed, claimFailed := failure.Get(); claimFailed {
		return nil, errors.WithStack(failed)
	}
	if err != nil {
		return nil, scriptError(err, "JavaScript function of "+replacement.String()+" threw an exception")
	}
	rows, err := e.rows(result)
	return rows, errors.Wrapf(err, "result of %s", replacement)
}

// SplitStatement passes the replies a splitting replacement's using function offers for
// a reference statement's values and rows to offer.
func (e *Evaluator) SplitStatement(limit time.Duration, replacement StatementReplacement, values []Option[string], rows []Row, offer Offer) error {
	using, ok := e.registry.statementUser(replacement.Index())
	if !ok {
		return errors.Errorf("JavaScript function of %s is unavailable", replacement)
	}
	clock := startStopwatch(e.runtime, limit)
	defer clock.stop()
	failure := None[error]()
	functions := make([]sobek.Value, 0, len(replacement.Statements()))
	for index := range replacement.Statements() {
		functions = append(functions, e.runtime.ToValue(func(call sobek.FunctionCall) sobek.Value {
			var offered []Option[string]
			var offeredRows []Row
			err := errors.New("offer requires rows after its values")
			if last := len(call.Arguments) - 1; last >= 0 {
				offered, err = e.statementValues(call.Arguments[:last])
				if err == nil {
					offeredRows, err = e.rows(call.Arguments[last])
				}
			}
			if err == nil {
				err = offer(index, offered, offeredRows)
			}
			if err != nil {
				failure = Some(err)
				panic(e.runtime.NewGoError(err))
			}
			return sobek.Undefined()
		}))
	}
	_, err := using(sobek.Undefined(), e.valuesValue(values), e.rowsValue(rows), e.array(functions))
	if failed, offerFailed := failure.Get(); offerFailed {
		return errors.WithStack(failed)
	}
	if err != nil {
		return scriptError(err, "JavaScript function of "+replacement.String()+" threw an exception")
	}
	return nil
}

func (e *Evaluator) valuesValue(values []Option[string]) sobek.Value {
	elements := make([]sobek.Value, 0, len(values))
	for _, value := range values {
		elements = append(elements, e.nullable(value))
	}
	return e.array(elements)
}

func (e *Evaluator) rowsValue(rows []Row) sobek.Value {
	elements := make([]sobek.Value, 0, len(rows))
	for _, row := range rows {
		object := e.runtime.NewObject()
		for label, value := range row {
			_ = object.Set(label, e.nullable(value)) //nolint:errcheck // Plain objects accept any property.
		}
		elements = append(elements, object)
	}
	return e.array(elements)
}

// array returns a JavaScript array of the elements.
func (e *Evaluator) array(elements []sobek.Value) sobek.Value {
	array := e.runtime.NewArray()
	for index, element := range elements {
		_ = array.Set(strconv.Itoa(index), element) //nolint:errcheck // Arrays accept any index.
	}
	return array
}

func (e *Evaluator) nullable(value Option[string]) sobek.Value {
	if text, ok := value.Get(); ok {
		return e.runtime.ToValue(text)
	}
	return sobek.Null()
}

// statementValues reads the statement values a script passes as arguments.
func (e *Evaluator) statementValues(arguments []sobek.Value) ([]Option[string], error) {
	values := make([]Option[string], 0, len(arguments))
	for _, argument := range arguments {
		text, err := sqlText(argument)
		if err != nil {
			return nil, err
		}
		values = append(values, text)
	}
	return values, nil
}

// rows reads a script's array of row objects.
func (e *Evaluator) rows(value sobek.Value) ([]Row, error) {
	array, ok := value.(*sobek.Object)
	if !ok || array.ClassName() != "Array" {
		return nil, errors.New("rows must be an array")
	}
	length := array.Get("length").ToInteger()
	rows := make([]Row, 0, length)
	for index := range length {
		object, isObject := array.Get(strconv.FormatInt(index, 10)).(*sobek.Object)
		if !isObject || object.ClassName() != "Object" {
			return nil, errors.New("each row must be an object")
		}
		row := Row{}
		for _, label := range object.Keys() {
			text, err := sqlText(object.Get(label))
			if err != nil {
				return nil, errors.Wrapf(err, "row field %q", label)
			}
			row[label] = text
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// sqlText is a script value as MySQL's text protocol sends it, with null as NULL.
func sqlText(value sobek.Value) (Option[string], error) {
	switch value := value.Export().(type) {
	case nil:
		return None[string](), nil
	case string:
		return Some(value), nil
	case int64:
		return Some(strconv.FormatInt(value, 10)), nil
	case float64:
		return Some(strconv.FormatFloat(value, 'f', -1, 64)), nil
	case bool:
		if value {
			return Some("1"), nil
		}
		return Some("0"), nil
	}
	return None[string](), errors.Errorf("value of type %T is not a string, number, boolean or null", value.Export())
}

// normalise returns an undefined result as None so callers can remove the node.
func (e *Evaluator) normalise(callback sobek.Callable, value Option[model.DynamicValue]) (Option[model.DynamicValue], error) {
	argument, err := e.argument(value)
	if err != nil {
		return None[model.DynamicValue](), errors.Wrap(err, "clone normaliser argument")
	}
	result, err := callback(sobek.Undefined(), argument)
	if err != nil {
		return None[model.DynamicValue](), scriptError(err, "JavaScript normaliser threw an exception")
	}
	if sobek.IsUndefined(result) {
		return None[model.DynamicValue](), nil
	}
	normalised, err := e.result(result)
	if err != nil {
		return None[model.DynamicValue](), err
	}
	return Some(normalised), nil
}

func (e *Evaluator) argument(optionalValue Option[model.DynamicValue]) (sobek.Value, error) {
	value, present := optionalValue.Get()
	if !present {
		return sobek.Undefined(), nil
	}
	// A JSON round trip prevents JavaScript mutation from reaching Go-owned documents.
	data, err := json.Marshal(value)
	if err != nil {
		return nil, errors.Wrap(err, "encode normaliser argument")
	}
	parsed, err := e.parse(sobek.Undefined(), e.runtime.ToValue(string(data)))
	return parsed, errors.Wrap(err, "parse normaliser argument")
}

// result detaches a normalised value from the runtime through the JSON helper.
func (e *Evaluator) result(value sobek.Value) (model.DynamicValue, error) {
	encoded, err := e.stringify(sobek.Undefined(), value)
	if err != nil {
		return nil, errors.New("JavaScript normaliser returned a value that is not representable as JSON")
	}
	text, ok := encoded.Export().(string)
	if !ok {
		return nil, errors.New("JavaScript normaliser returned a value that is not representable as JSON")
	}
	var normalised model.DynamicValue
	if err := json.Unmarshal([]byte(text), &normalised); err != nil {
		return nil, errors.Wrap(err, "decode normalised value")
	}
	return normalised, nil
}

// scriptError hides the exception but keeps an interruption so callers can see cancellation.
func scriptError(err error, message string) error {
	if interrupted, ok := errors.AsType[*sobek.InterruptedError](err); ok {
		return errors.Wrap(interrupted, message)
	}
	return errors.New(message)
}

func watchRuntime(ctx context.Context, runtime *sobek.Runtime) func() {
	interrupted := make(chan struct{})
	// Interrupt is concurrency-safe, but all other runtime access stays on the caller.
	stop := context.AfterFunc(ctx, func() {
		runtime.Interrupt(ctx.Err())
		close(interrupted)
	})
	return func() {
		if !stop() {
			<-interrupted
		}
	}
}

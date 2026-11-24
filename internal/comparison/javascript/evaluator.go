package javascript

import (
	"context"
	"encoding/json"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/schema"
)

// Evaluator owns the JavaScript runtime for one payload normalisation.
// It is confined to its caller because Sobek runtime access is not synchronized.
type Evaluator struct {
	runtime   *sobek.Runtime
	parse     sobek.Callable
	stringify sobek.Callable
	registry  *callbackRegistry
	// clock limits the time that bindings' callbacks run for.
	clock *stopwatch
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
	limit time.Duration,
) (*Evaluator, registrations, error) {
	runtime := sobek.New()
	runtime.SetMaxCallStackSize(1024)
	evaluator := &Evaluator{runtime: runtime, clock: newStopwatch(runtime, limit), stop: Some(watchRuntime(ctx, runtime))}
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
	registry.attach(evaluator)
	return evaluator, registry.registrations(), nil
}

// Close releases the evaluator's context watcher and stops its clock.
func (e *Evaluator) Close() {
	e.clock.pause()
	stop, ok := e.stop.Get()
	if !ok {
		return
	}
	stop()
	e.stop = None[func()]()
}

// NormaliseField invokes the normaliser registered for a JSON field path. None
// stands for an absent field in both the argument and the result.
func (e *Evaluator) NormaliseField(target FieldTarget, value Option[schema.Value]) (Option[schema.Value], error) {
	callback, ok := e.registry.field(target)
	if !ok {
		return None[schema.Value](), errors.Errorf("JavaScript field normaliser (%q, %q) is unavailable", target.Type(), target.Path())
	}
	return e.normalise(callback, value)
}

// NormaliseMessage invokes the normaliser registered for an object type. None
// stands for an absent message in both the argument and the result.
func (e *Evaluator) NormaliseMessage(target schema.TypeRef, value Option[schema.Value]) (Option[schema.Value], error) {
	callback, ok := e.registry.message(target)
	if !ok {
		return None[schema.Value](), errors.Errorf("JavaScript message normaliser %q is unavailable", target)
	}
	return e.normalise(callback, value)
}

// count counts script time against the evaluator's limit until the returned function is called.
func (e *Evaluator) count() (stop func()) {
	return e.clock.count()
}

// exclude stops counting script time against the evaluator's limit until the returned function is called.
func (e *Evaluator) exclude() (resume func()) {
	return e.clock.exclude()
}

// boundMembers returns each binding's members in the evaluator's runtime.
func (e *Evaluator) boundMembers() []boundMembers {
	return e.registry.boundMembers()
}

// ReplaceRequest maps a request to a replacing endpoint to the source request it pairs with.
func (e *Evaluator) ReplaceRequest(replacement Replacement, request schema.Value) (schema.Value, error) {
	callbacks, ok := e.registry.replacer(replacement.Index())
	if !ok {
		return nil, errors.Errorf("JavaScript request function of %s is unavailable", replacement)
	}
	return e.replace(replacement, "request", callbacks.request, request)
}

// ReplaceResponse builds the answer to a request to a replacing endpoint from a
// reference response to its source.
func (e *Evaluator) ReplaceResponse(replacement Replacement, request, reference schema.Value) (schema.Value, error) {
	callbacks, ok := e.registry.replacer(replacement.Index())
	if !ok {
		return nil, errors.Errorf("JavaScript response function of %s is unavailable", replacement)
	}
	return e.replace(replacement, "response", callbacks.response, request, reference)
}

func (e *Evaluator) replace(replacement Replacement, function string, callback sobek.Callable, values ...schema.Value) (schema.Value, error) {
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

// normalise returns an undefined result as None so callers can remove the node.
func (e *Evaluator) normalise(callback sobek.Callable, value Option[schema.Value]) (Option[schema.Value], error) {
	argument, err := e.argument(value)
	if err != nil {
		return None[schema.Value](), errors.Wrap(err, "clone normaliser argument")
	}
	return e.invoke(callback, argument)
}

// call passes values to a script callback as JSON and returns its result as JSON, with an
// undefined result as None.
func (e *Evaluator) call(callback sobek.Callable, values []schema.Value) (Option[schema.Value], error) {
	arguments := make([]sobek.Value, 0, len(values))
	for _, value := range values {
		argument, err := e.argument(Some(value))
		if err != nil {
			return None[schema.Value](), errors.Wrap(err, "clone normaliser argument")
		}
		arguments = append(arguments, argument)
	}
	return e.invoke(callback, arguments...)
}

func (e *Evaluator) invoke(callback sobek.Callable, arguments ...sobek.Value) (Option[schema.Value], error) {
	result, err := callback(sobek.Undefined(), arguments...)
	if err != nil {
		return None[schema.Value](), scriptError(err, "JavaScript normaliser threw an exception")
	}
	if sobek.IsUndefined(result) {
		return None[schema.Value](), nil
	}
	normalised, err := e.result(result)
	if err != nil {
		return None[schema.Value](), err
	}
	return Some(normalised), nil
}

func (e *Evaluator) argument(optionalValue Option[schema.Value]) (sobek.Value, error) {
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
// Scripts return unknown, so the value is of type unknown.
func (e *Evaluator) result(value sobek.Value) (schema.Value, error) {
	encoded, err := e.stringify(sobek.Undefined(), value)
	if err != nil {
		return nil, errors.New("JavaScript normaliser returned a value that is not representable as JSON")
	}
	text, ok := encoded.Export().(string)
	if !ok {
		return nil, errors.New("JavaScript normaliser returned a value that is not representable as JSON")
	}
	normalised, err := schema.Decode(schema.UnknownType{}, []byte(text))
	return normalised, errors.Wrap(err, "decode normalised value")
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

package javascript

import (
	"context"
	"encoding/json"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"
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
	helpers, err := runtime.RunString(jsonHelpers)
	if err != nil {
		evaluator.Close()
		return nil, registrations{}, errors.Wrap(err, "create JavaScript JSON helpers")
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
		evaluator.Close()
		return nil, registrations{}, err
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
func (e *Evaluator) NormaliseField(target FieldTarget, value Option[any]) (Option[any], error) {
	callback, ok := e.registry.field(target)
	if !ok {
		return None[any](), errors.Errorf("JavaScript field normaliser (%q, %q) is unavailable", target.Type(), target.Path())
	}
	return e.normalise(callback, value)
}

// NormaliseMessage invokes the normaliser registered for an object type. None
// stands for an absent message in both the argument and the result.
func (e *Evaluator) NormaliseMessage(target string, value Option[any]) (Option[any], error) {
	callback, ok := e.registry.message(target)
	if !ok {
		return None[any](), errors.Errorf("JavaScript message normaliser %q is unavailable", target)
	}
	return e.normalise(callback, value)
}

// normalise returns an undefined result as None so callers can remove the node.
func (e *Evaluator) normalise(callback sobek.Callable, value Option[any]) (Option[any], error) {
	argument, err := e.argument(value)
	if err != nil {
		return None[any](), errors.Wrap(err, "clone normaliser argument")
	}
	result, err := callback(sobek.Undefined(), argument)
	if err != nil {
		return None[any](), errors.New("JavaScript normaliser threw an exception")
	}
	if sobek.IsUndefined(result) {
		return None[any](), nil
	}
	normalised, err := e.result(result)
	if err != nil {
		return None[any](), err
	}
	return Some(normalised), nil
}

func (e *Evaluator) argument(optionalValue Option[any]) (sobek.Value, error) {
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
func (e *Evaluator) result(value sobek.Value) (any, error) {
	encoded, err := e.stringify(sobek.Undefined(), value)
	if err != nil {
		return nil, errors.New("JavaScript normaliser returned a value that is not representable as JSON")
	}
	text, ok := encoded.Export().(string)
	if !ok {
		return nil, errors.New("JavaScript normaliser returned a value that is not representable as JSON")
	}
	var normalised any
	if err := json.Unmarshal([]byte(text), &normalised); err != nil {
		return nil, errors.Wrap(err, "decode normalised value")
	}
	return normalised, nil
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

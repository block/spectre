package javascript

import (
	"context"
	"encoding/json"

	"github.com/alecthomas/errors"
	"github.com/grafana/sobek"
)

// Evaluator owns the JavaScript runtime for one payload normalisation.
// It is confined to its caller because Sobek runtime access is not synchronized.
type Evaluator struct {
	runtime   *sobek.Runtime
	parse     sobek.Callable
	stringify sobek.Callable
	registry  *callbackRegistry
	stop      func()
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
) (evaluator *Evaluator, fields, messages, rpcs []string, err error) {
	runtime := sobek.New()
	runtime.SetMaxCallStackSize(1024)
	evaluator = &Evaluator{runtime: runtime, stop: watchRuntime(ctx, runtime)}
	helpers, err := runtime.RunString(jsonHelpers)
	if err != nil {
		evaluator.Close()
		return nil, nil, nil, nil, errors.Wrap(err, "create JavaScript JSON helpers")
	}
	helperObject := helpers.ToObject(runtime)
	evaluator.parse, _ = sobek.AssertFunction(helperObject.Get("parse"))
	evaluator.stringify, _ = sobek.AssertFunction(helperObject.Get("stringify"))
	if evaluator.parse == nil || evaluator.stringify == nil {
		evaluator.Close()
		return nil, nil, nil, nil, errors.New("JavaScript JSON helpers are not callable")
	}
	registry, err := loadSpectreModule(runtime, entry, spectre)
	if err != nil {
		evaluator.Close()
		return nil, nil, nil, nil, err
	}
	evaluator.registry = registry
	return evaluator,
		registry.targets(targetField),
		registry.targets(targetMessage),
		registry.targets(targetRPC),
		nil
}

// Close releases the evaluator's context watcher.
func (e *Evaluator) Close() {
	if e.stop == nil {
		return
	}
	e.stop()
	e.stop = nil
}

// NormaliseField invokes the normaliser registered for a protobuf field.
func (e *Evaluator) NormaliseField(
	target string,
	value any,
	present bool,
) (normalised any, normalisedPresent bool, err error) {
	return e.normalise(targetField, target, value, present)
}

// NormaliseMessage invokes the normaliser registered for a protobuf message.
func (e *Evaluator) NormaliseMessage(
	target string,
	value any,
	present bool,
) (normalised any, normalisedPresent bool, err error) {
	return e.normalise(targetMessage, target, value, present)
}

// NormaliseRPC invokes the normaliser registered for an RPC method.
func (e *Evaluator) NormaliseRPC(
	target string,
	value any,
	present bool,
) (normalised any, normalisedPresent bool, err error) {
	return e.normalise(targetRPC, target, value, present)
}

// normalise returns an undefined result as absent so callers can remove the node.
func (e *Evaluator) normalise(
	kind targetKind,
	target string,
	value any,
	present bool,
) (normalised any, normalisedPresent bool, err error) {
	callback, ok := e.registry.lookup(kind, target)
	if !ok {
		return nil, false, errors.Errorf("JavaScript normaliser %q is unavailable", target)
	}
	argument, err := e.argument(value, present)
	if err != nil {
		return nil, false, errors.Wrap(err, "clone normaliser argument")
	}
	result, err := callback(sobek.Undefined(), argument)
	if err != nil {
		return nil, false, errors.New("JavaScript normaliser threw an exception")
	}
	if sobek.IsUndefined(result) {
		return nil, false, nil
	}
	normalised, err = e.result(result)
	if err != nil {
		return nil, false, err
	}
	return normalised, true, nil
}

func (e *Evaluator) argument(value any, present bool) (sobek.Value, error) {
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

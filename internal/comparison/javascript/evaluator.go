package javascript

import (
	"context"
	"encoding/json"

	"github.com/alecthomas/errors"
	"github.com/grafana/sobek"
)

// Evaluator owns the JavaScript runtime for one response comparison.
// It is confined to its caller because Sobek runtime access is not synchronized.
type Evaluator struct {
	runtime  *sobek.Runtime
	parse    sobek.Callable
	registry *callbackRegistry
	stop     func()
}

// newEvaluator builds a runtime from the exact compiled values owned by Program.
func newEvaluator(
	ctx context.Context,
	entry *sobek.SourceTextModuleRecord,
	spectre *spectreModule,
) (evaluator *Evaluator, fields, messages, rpcs []string, err error) {
	runtime := sobek.New()
	runtime.SetMaxCallStackSize(1024)
	evaluator = &Evaluator{runtime: runtime, stop: watchRuntime(ctx, runtime)}
	parse, err := runtime.RunString(`(function(parse) {
		return function(text) { return parse(text); };
	})(JSON.parse)`)
	if err != nil {
		evaluator.Close()
		return nil, nil, nil, nil, errors.Wrap(err, "create JavaScript parse helper")
	}
	evaluator.parse, _ = sobek.AssertFunction(parse)
	if evaluator.parse == nil {
		evaluator.Close()
		return nil, nil, nil, nil, errors.New("JavaScript parse helper is not callable")
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

// CompareField invokes the comparator registered for a protobuf field.
func (e *Evaluator) CompareField(
	target string,
	reference any,
	referencePresent bool,
	candidate any,
	candidatePresent bool,
) (matched bool, err error) {
	return e.compare(targetField, target, reference, referencePresent, candidate, candidatePresent)
}

// CompareMessage invokes the comparator registered for a protobuf message.
func (e *Evaluator) CompareMessage(
	target string,
	reference any,
	referencePresent bool,
	candidate any,
	candidatePresent bool,
) (matched bool, err error) {
	return e.compare(targetMessage, target, reference, referencePresent, candidate, candidatePresent)
}

// CompareRPC invokes the comparator registered for an RPC method.
func (e *Evaluator) CompareRPC(
	target string,
	reference any,
	referencePresent bool,
	candidate any,
	candidatePresent bool,
) (matched bool, err error) {
	return e.compare(targetRPC, target, reference, referencePresent, candidate, candidatePresent)
}

func (e *Evaluator) compare(
	kind targetKind,
	target string,
	reference any,
	referencePresent bool,
	candidate any,
	candidatePresent bool,
) (matched bool, err error) {
	callback, ok := e.registry.lookup(kind, target)
	if !ok {
		return false, errors.Errorf("JavaScript comparator %q is unavailable", target)
	}
	referenceArgument, err := e.argument(reference, referencePresent)
	if err != nil {
		return false, errors.Wrap(err, "clone reference comparator argument")
	}
	candidateArgument, err := e.argument(candidate, candidatePresent)
	if err != nil {
		return false, errors.Wrap(err, "clone candidate comparator argument")
	}
	result, err := callback(sobek.Undefined(), referenceArgument, candidateArgument)
	if err != nil {
		return false, errors.New("JavaScript comparator threw an exception")
	}
	matched, ok = result.Export().(bool)
	if !ok {
		return false, errors.New("JavaScript comparator returned a non-boolean value")
	}
	return matched, nil
}

func (e *Evaluator) argument(value any, present bool) (sobek.Value, error) {
	if !present {
		return sobek.Undefined(), nil
	}
	// A JSON round trip prevents JavaScript mutation from reaching Go-owned documents.
	data, err := json.Marshal(value)
	if err != nil {
		return nil, errors.Wrap(err, "encode comparator argument")
	}
	parsed, err := e.parse(sobek.Undefined(), e.runtime.ToValue(string(data)))
	return parsed, errors.Wrap(err, "parse comparator argument")
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

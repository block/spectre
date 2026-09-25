package javascript

import (
	"sort"

	"github.com/alecthomas/errors"
	"github.com/grafana/sobek"
)

type targetKind string

const (
	targetField   targetKind = "field"
	targetMessage targetKind = "message"
	targetRPC     targetKind = "rpc"
)

// spectreModule is a stateless module record shared safely across isolated runtimes.
type spectreModule struct{}

var _ sobek.ModuleRecord = (*spectreModule)(nil)

func (m *spectreModule) Link() error {
	return nil
}

func (m *spectreModule) GetExportedNames(callback func([]string), _ ...sobek.ModuleRecord) bool {
	callback([]string{string(targetField), string(targetMessage), string(targetRPC)})
	return true
}

func (m *spectreModule) ResolveExport(name string, _ ...sobek.ResolveSetElement) (*sobek.ResolvedBinding, bool) {
	kind := targetKind(name)
	if kind != targetField && kind != targetMessage && kind != targetRPC {
		return nil, false
	}
	return &sobek.ResolvedBinding{Module: m, BindingName: name}, false
}

func (m *spectreModule) Evaluate(runtime *sobek.Runtime) *sobek.Promise {
	promise, resolve, _ := runtime.NewPromise()
	instance := newCallbackRegistry(runtime)
	if err := resolve(instance); err != nil {
		panic(errors.Wrap(err, "resolve spectre module evaluation"))
	}
	return promise
}

// callbackRegistry is one runtime's module instance and owns all callable values.
// Evaluator access stays on the runtime's single caller, so it needs no locking.
type callbackRegistry struct {
	runtime  *sobek.Runtime
	fields   map[string]sobek.Callable
	messages map[string]sobek.Callable
	rpcs     map[string]sobek.Callable
}

func newCallbackRegistry(runtime *sobek.Runtime) *callbackRegistry {
	return &callbackRegistry{
		runtime:  runtime,
		fields:   map[string]sobek.Callable{},
		messages: map[string]sobek.Callable{},
		rpcs:     map[string]sobek.Callable{},
	}
}

func (r *callbackRegistry) GetBindingValue(name string) sobek.Value {
	kind := targetKind(name)
	return r.runtime.ToValue(func(call sobek.FunctionCall) sobek.Value {
		target, ok := call.Argument(0).Export().(string)
		if !ok || target == "" {
			panic(r.runtime.NewTypeError("spectre.%s target must be a non-empty string", kind))
		}
		callback, ok := sobek.AssertFunction(call.Argument(1))
		if !ok {
			panic(r.runtime.NewTypeError("spectre.%s comparator must be a function", kind))
		}
		if !r.register(kind, target, callback) {
			panic(r.runtime.NewTypeError("duplicate comparator target %q", target))
		}
		return sobek.Undefined()
	})
}

func (r *callbackRegistry) register(kind targetKind, target string, callback sobek.Callable) bool {
	callbacks := r.callbacks(kind)
	if _, duplicate := callbacks[target]; duplicate {
		return false
	}
	callbacks[target] = callback
	return true
}

func (r *callbackRegistry) lookup(kind targetKind, target string) (sobek.Callable, bool) {
	callback, ok := r.callbacks(kind)[target]
	return callback, ok
}

func (r *callbackRegistry) targets(kind targetKind) []string {
	callbacks := r.callbacks(kind)
	targets := make([]string, 0, len(callbacks))
	for target := range callbacks {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}

func (r *callbackRegistry) callbacks(kind targetKind) map[string]sobek.Callable {
	switch kind {
	case targetField:
		return r.fields
	case targetMessage:
		return r.messages
	case targetRPC:
		return r.rpcs
	default:
		return nil
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

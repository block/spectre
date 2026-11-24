package script

import (
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/schema"
)

// runtimeCallbacks are one runtime's mapping functions, indexed like the declared mappings.
type runtimeCallbacks struct {
	scope     *javascript.Scope
	functions []sobek.Callable
}

func newRuntimeCallbacks(scope *javascript.Scope) *runtimeCallbacks {
	return &runtimeCallbacks{scope: scope}
}

// add records a mapping function and returns its index.
func (r *runtimeCallbacks) add(function sobek.Callable) int {
	r.functions = append(r.functions, function)
	return len(r.functions) - 1
}

// mapValue runs a mapping function on JSON decoded as its From type.
func (r *runtimeCallbacks) mapValue(mapping Mapping, data []byte) (schema.Value, error) {
	function, ok := mapping.function(r.functions)
	if !ok {
		return nil, errors.Errorf("JavaScript function mapping %s to %s is unavailable", mapping.From, mapping.To)
	}
	loaded := r.scope.Schema()
	from, err := loaded.Type(mapping.From)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	to, err := loaded.Type(mapping.To)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	value, err := schema.Decode(from, data)
	if err != nil {
		return nil, errors.Wrapf(err, "decode %s", mapping.From)
	}
	mapped, err := r.scope.Call(function, value)
	if err != nil {
		return nil, errors.Wrapf(err, "map %s to %s", mapping.From, mapping.To)
	}
	result, ok := mapped.Get()
	if !ok {
		return nil, errors.Errorf("JavaScript function mapping %s to %s returned undefined", mapping.From, mapping.To)
	}
	typed, err := schema.As(to, result)
	return typed, errors.Wrapf(err, "result of mapping %s to %s", mapping.From, mapping.To)
}

// Callbacks are the mapping functions in one evaluator's runtime, so they are confined to its
// caller. The zero value stands for an evaluator that was not built with the binding.
type Callbacks struct {
	runtime Option[*runtimeCallbacks]
}

func newCallbacks(runtime *runtimeCallbacks) Callbacks {
	return Callbacks{runtime: Some(runtime)}
}

// Map decodes JSON as the mapping's From type, maps it, and checks the result is its To type.
func (c Callbacks) Map(mapping Mapping, data []byte) (schema.Value, error) {
	runtime, ok := c.runtime.Get()
	if !ok {
		return nil, errors.Errorf("JavaScript function mapping %s to %s is unavailable", mapping.From, mapping.To)
	}
	return runtime.mapValue(mapping, data)
}

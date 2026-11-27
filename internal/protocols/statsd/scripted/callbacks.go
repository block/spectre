package scripted

import (
	"maps"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/protocols/statsd/metric"
)

// Renamed is the series that a rename callback returned, and the factor for its values.
type Renamed struct {
	Series metric.Series
	Scale  float64
}

// runtimeCallbacks are one runtime's rename and tag callbacks, indexed as the rules name them.
type runtimeCallbacks struct {
	scope   *javascript.Scope
	renames []sobek.Callable
	selects []sobek.Callable
}

func newRuntimeCallbacks(scope *javascript.Scope) *runtimeCallbacks {
	return &runtimeCallbacks{scope: scope}
}

// Callbacks are the rules' callbacks in one evaluator's runtime, so they are confined to its
// caller. The zero value stands for an evaluator that was not built with the binding.
type Callbacks struct {
	runtime Option[*runtimeCallbacks]
}

func newCallbacks(runtime *runtimeCallbacks) Callbacks {
	return Callbacks{runtime: Some(runtime)}
}

// Selects reports whether a tag callback accepts the tag's value, which is absent when the series has no such tag.
func (c Callbacks) Selects(index int, value Option[string]) (bool, error) {
	runtime, ok := c.runtime.Get()
	if !ok || index < 0 || index >= len(runtime.selects) {
		return false, errors.Errorf("StatsD tag function %d is unavailable", index)
	}
	argument := sobek.Undefined()
	if text, present := value.Get(); present {
		argument = runtime.scope.Runtime().ToValue(text)
	}
	read := func(result sobek.Value) (bool, error) { return result.ToBoolean(), nil }
	selected, err := runtime.scope.Invoke(runtime.selects[index], "StatsD tag function threw an exception", read, argument)
	return selected, errors.WithStack(err)
}

// Rename returns what a rename callback makes of series from side, or nothing when it ignores the series.
func (c Callbacks) Rename(index int, side metric.Side, series metric.Series) (Option[Renamed], error) {
	runtime, ok := c.runtime.Get()
	if !ok || index < 0 || index >= len(runtime.renames) {
		return None[Renamed](), errors.Errorf("StatsD rename function %d is unavailable", index)
	}
	js := runtime.scope.Runtime()
	tags := js.NewObject()
	for name, value := range series.Tags {
		_ = tags.Set(name, value) //nolint:errcheck // Plain objects accept any property.
	}
	argument := js.NewObject()
	for property, value := range map[string]sobek.Value{
		"name": js.ToValue(series.Name), "type": js.ToValue(string(series.Type)), "tags": tags, "side": js.ToValue(string(side)),
	} {
		_ = argument.Set(property, value) //nolint:errcheck // Plain objects accept any property.
	}
	read := func(result sobek.Value) (Option[Renamed], error) { return readRenamed(js, series, result) }
	renamed, err := runtime.scope.Invoke(runtime.renames[index], "StatsD rename function threw an exception", read, argument)
	return renamed, errors.WithStack(err)
}

// readRenamed reads a RenamedMetric, which keeps the original's type, and its tags unless it names its own.
func readRenamed(js *sobek.Runtime, original metric.Series, result sobek.Value) (Option[Renamed], error) {
	if sobek.IsUndefined(result) || sobek.IsNull(result) {
		return None[Renamed](), nil
	}
	object := result.ToObject(js)
	name, isString := object.Get("name").Export().(string)
	if !isString || name == "" {
		return None[Renamed](), errors.New("StatsD rename function must return a non-empty name")
	}
	renamed := Renamed{Series: metric.Series{Name: name, Type: original.Type, Tags: maps.Clone(original.Tags)}, Scale: 1}
	if tags := object.Get("tags"); tags != nil && !sobek.IsUndefined(tags) {
		tagObject := tags.ToObject(js)
		renamed.Series.Tags = map[string]string{}
		for _, key := range tagObject.Keys() {
			renamed.Series.Tags[key] = tagObject.Get(key).String()
		}
	}
	if scale := object.Get("scale"); scale != nil && !sobek.IsUndefined(scale) {
		renamed.Scale = scale.ToFloat()
	}
	return Some(renamed), nil
}

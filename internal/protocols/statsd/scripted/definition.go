package scripted

import (
	"slices"
	"strings"

	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/protocols/statsd/metric"
)

// The rule object and its methods, as scripts name them.
const (
	ruleObject    = "EgressStatsDRule"
	tagMethod     = "tag"
	mapMethod     = "map"
	compareMethod = "compare"
	ignoreMethod  = "ignore"
	// qualifiedCompare prefixes the comparison's errors.
	qualifiedCompare = "spectre." + ruleObject + "." + compareMethod
)

// definition defines the StatsD binding's members in one runtime.
type definition struct {
	scope     *javascript.Scope
	runtime   *sobek.Runtime
	declared  *Declarations
	callbacks *runtimeCallbacks
}

func define(scope *javascript.Scope) (javascript.Members[Declarations, Callbacks], error) {
	return newDefinition(scope).members()
}

func newDefinition(scope *javascript.Scope) *definition {
	return &definition{scope: scope, runtime: scope.Runtime(), declared: &Declarations{}, callbacks: newRuntimeCallbacks(scope)}
}

// members defines egress.statsd and returns it.
func (d *definition) members() (javascript.Members[Declarations, Callbacks], error) {
	statsd := d.runtime.ToValue(func(call sobek.FunctionCall) sobek.Value {
		const qualified = "egress.statsd"
		if len(call.Arguments) != 1 {
			panic(d.runtime.NewTypeError("spectre.%s requires a pattern", qualified))
		}
		pattern := d.scope.StringArgument(call, 0, qualified, "pattern")
		if _, err := PatternExpression(pattern); err != nil {
			panic(d.runtime.NewTypeError("spectre.%s: %v", qualified, err))
		}
		return d.rule(Selector{Pattern: pattern})
	})
	return javascript.Members[Declarations, Callbacks]{
		Egress: Some(statsd),
		Declared: func() Declarations {
			return Declarations{Rules: slices.Clone(d.declared.Rules)}
		},
		Instance: newCallbacks(d.callbacks),
	}, nil
}

// rule is the object that narrows selector and declares what happens to what it selects.
func (d *definition) rule(selector Selector) sobek.Value {
	object := d.runtime.NewObject()
	d.defineMethod(object, tagMethod, func(call sobek.FunctionCall) sobek.Value {
		const qualified = ruleObject + "." + tagMethod
		if len(call.Arguments) != 2 {
			panic(d.runtime.NewTypeError("spectre.%s requires a name and a value or function", qualified))
		}
		name := d.scope.StringArgument(call, 0, qualified, "name")
		var tag TagSelector
		if value, isString := call.Argument(1).Export().(string); isString {
			tag = TagValue{Name: name, Value: value}
		} else if callback, isFunction := sobek.AssertFunction(call.Argument(1)); isFunction {
			tag = TagScript{Name: name, Callback: len(d.callbacks.selects)}
			d.callbacks.selects = append(d.callbacks.selects, callback)
		} else {
			panic(d.runtime.NewTypeError("spectre.%s requires a string or a function", qualified))
		}
		narrowed := Selector{Pattern: selector.Pattern, Tags: append(slices.Clone(selector.Tags), tag)}
		return d.rule(narrowed)
	})
	d.defineMethod(object, mapMethod, func(call sobek.FunctionCall) sobek.Value {
		mapping, isFunction := sobek.AssertFunction(call.Argument(0))
		if len(call.Arguments) != 1 || !isFunction {
			panic(d.runtime.NewTypeError("spectre.%s.%s requires a function", ruleObject, mapMethod))
		}
		d.declare(Rule{Selector: selector, Action: ActionMap{Callback: len(d.callbacks.mappings)}})
		d.callbacks.mappings = append(d.callbacks.mappings, mapping)
		return sobek.Undefined()
	})
	d.defineMethod(object, compareMethod, func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) != 1 {
			panic(d.runtime.NewTypeError("spectre.%s.%s requires a comparison", ruleObject, compareMethod))
		}
		d.declare(Rule{Selector: selector, Action: ActionCompare{Comparison: d.comparison(call.Argument(0))}})
		return sobek.Undefined()
	})
	d.defineMethod(object, ignoreMethod, func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) != 0 {
			panic(d.runtime.NewTypeError("spectre.%s.%s takes no arguments", ruleObject, ignoreMethod))
		}
		d.declare(Rule{Selector: selector, Action: ActionIgnore{}})
		return sobek.Undefined()
	})
	return object
}

func (d *definition) declare(rule Rule) {
	d.declared.Rules = append(d.declared.Rules, rule)
}

// comparison reads a MetricComparison object.
func (d *definition) comparison(value sobek.Value) Comparison {
	if sobek.IsUndefined(value) || sobek.IsNull(value) {
		panic(d.runtime.NewTypeError(qualifiedCompare + " requires a comparison"))
	}
	object := value.ToObject(d.runtime)
	statistics := metric.Statistics()
	names := make([]string, 0, len(statistics))
	for _, statistic := range statistics {
		names = append(names, string(statistic))
	}
	for _, key := range object.Keys() {
		if !slices.Contains(names, key) {
			panic(d.runtime.NewTypeError(qualifiedCompare+" does not compare %q; it compares %s", key, strings.Join(names, ", ")))
		}
	}
	comparison := Comparison{}
	for _, statistic := range statistics {
		if property := object.Get(string(statistic)); property != nil && !sobek.IsUndefined(property) {
			comparison.Checks = append(comparison.Checks, Check{Statistic: statistic, Tolerance: d.tolerance(string(statistic), property)})
		}
	}
	if len(comparison.Checks) == 0 {
		panic(d.runtime.NewTypeError(qualifiedCompare+" requires at least one of %s", strings.Join(names, ", ")))
	}
	return comparison
}

// tolerance reads a statistic's Tolerance object, whose absent properties are 0.
func (d *definition) tolerance(statistic string, value sobek.Value) Tolerance {
	object, isObject := value.(*sobek.Object)
	if !isObject {
		panic(d.runtime.NewTypeError(qualifiedCompare+" %s requires a tolerance", statistic))
	}
	properties := []string{"upper", "lower", "slack"}
	for _, key := range object.Keys() {
		if !slices.Contains(properties, key) {
			panic(d.runtime.NewTypeError(qualifiedCompare+" %s does not take %q; it takes upper, lower and slack", statistic, key))
		}
	}
	number := func(property string) float64 {
		read := object.Get(property)
		if read == nil || sobek.IsUndefined(read) {
			return 0
		}
		number, isNumber := read.Export().(float64)
		if integer, isInteger := read.Export().(int64); isInteger {
			number, isNumber = float64(integer), true
		}
		if !isNumber || number < 0 {
			panic(d.runtime.NewTypeError(qualifiedCompare+" %s %s must be a number of at least 0", statistic, property))
		}
		return number
	}
	return Tolerance{Upper: number("upper"), Lower: number("lower"), Slack: number("slack")}
}

// defineMethod binds one object method, panicking if sobek rejects it.
func (d *definition) defineMethod(object *sobek.Object, method string, fn func(sobek.FunctionCall) sobek.Value) {
	if err := object.Set(method, fn); err != nil {
		panic(d.runtime.NewTypeError("define spectre.%s.%s: %v", ruleObject, method, err))
	}
}

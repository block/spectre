package scripted

import (
	"slices"
	"strconv"

	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/comparison/javascript"
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
		pattern := d.scope.StringArgument(call, 0, "egress.statsd", "pattern")
		if _, err := PatternExpression(pattern); err != nil {
			panic(d.runtime.NewTypeError("spectre.egress.statsd: %v", err))
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
	d.defineMethod(object, "tag", func(call sobek.FunctionCall) sobek.Value {
		name := d.scope.StringArgument(call, 0, "EgressStatsDRule.tag", "name")
		tag := TagSelector{Name: name, Value: None[string]()}
		if value, isString := call.Argument(1).Export().(string); isString {
			tag.Value = Some(value)
		} else if callback, isFunction := sobek.AssertFunction(call.Argument(1)); isFunction {
			tag.Callback = len(d.callbacks.selects)
			d.callbacks.selects = append(d.callbacks.selects, callback)
		} else {
			panic(d.runtime.NewTypeError("spectre.EgressStatsDRule.tag requires a string or a function"))
		}
		narrowed := Selector{Pattern: selector.Pattern, Tags: append(slices.Clone(selector.Tags), tag)}
		return d.rule(narrowed)
	})
	d.defineMethod(object, "map", func(call sobek.FunctionCall) sobek.Value {
		mapping, isFunction := sobek.AssertFunction(call.Argument(0))
		if !isFunction {
			panic(d.runtime.NewTypeError("spectre.EgressStatsDRule.map requires a function"))
		}
		d.declare(Rule{Selector: selector, Map: Some(len(d.callbacks.mappings)), Compare: None[Comparison]()})
		d.callbacks.mappings = append(d.callbacks.mappings, mapping)
		return sobek.Undefined()
	})
	d.defineMethod(object, "compare", func(call sobek.FunctionCall) sobek.Value {
		d.declare(Rule{Selector: selector, Map: None[int](), Compare: Some(d.comparison(call.Argument(0)))})
		return sobek.Undefined()
	})
	d.defineMethod(object, "ignore", func(sobek.FunctionCall) sobek.Value {
		d.declare(Rule{Selector: selector, Map: None[int](), Compare: None[Comparison]()})
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
		panic(d.runtime.NewTypeError("spectre.EgressStatsDRule.compare requires a comparison"))
	}
	object := value.ToObject(d.runtime)
	comparison := Comparison{Tolerance: d.tolerance("compare", object)}
	for _, rank := range []float64{50, 75, 90, 95, 99} {
		name := "p" + strconv.Itoa(int(rank))
		if property := object.Get(name); property != nil && !sobek.IsUndefined(property) {
			tolerance := d.tolerance("compare "+name, property.ToObject(d.runtime))
			comparison.Percentiles = append(comparison.Percentiles, Percentile{Rank: rank, Tolerance: tolerance})
		}
	}
	return comparison
}

// tolerance reads a Tolerance object's properties.
func (d *definition) tolerance(name string, object *sobek.Object) Tolerance {
	number := func(property string) float64 {
		value := object.Get(property)
		if value == nil || sobek.IsUndefined(value) {
			return 0
		}
		number, isNumber := value.Export().(float64)
		if integer, isInteger := value.Export().(int64); isInteger {
			number, isNumber = float64(integer), true
		}
		if !isNumber || number < 0 {
			panic(d.runtime.NewTypeError("spectre.EgressStatsDRule.%s %s must be a number of at least 0", name, property))
		}
		return number
	}
	tolerance := Tolerance{Relative: number("relative"), Absolute: number("absolute"), Only: Both}
	if only := object.Get("only"); only != nil && !sobek.IsUndefined(only) {
		bound := Bound(only.String())
		if bound != Higher && bound != Lower {
			panic(d.runtime.NewTypeError(`spectre.EgressStatsDRule.%s only must be "higher" or "lower"`, name))
		}
		tolerance.Only = bound
	}
	return tolerance
}

// defineMethod binds one object method, panicking if sobek rejects it.
func (d *definition) defineMethod(object *sobek.Object, method string, fn func(sobek.FunctionCall) sobek.Value) {
	if err := object.Set(method, fn); err != nil {
		panic(d.runtime.NewTypeError("define spectre.EgressStatsDRule.%s: %v", method, err))
	}
}

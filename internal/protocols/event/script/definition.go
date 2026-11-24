package script

import (
	"slices"

	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
)

const (
	triggersMethod = "triggers"
	optionalMethod = "optional"
	matchMethod    = "match"
	pairsMethod    = "pairs"
	payloadMethod  = "payload"
	resultMethod   = "result"
)

// definition defines the event binding's members in one runtime.
type definition struct {
	scope     *javascript.Scope
	runtime   *sobek.Runtime
	declared  *Declarations
	callbacks *runtimeCallbacks
	// types holds every reference event type a rule has declared, so each has one rule.
	types map[string]bool
}

func define(scope *javascript.Scope) (javascript.Members[Declarations, Callbacks], error) {
	return newDefinition(scope).members()
}

func newDefinition(scope *javascript.Scope) *definition {
	return &definition{
		scope:     scope,
		runtime:   scope.Runtime(),
		declared:  &Declarations{},
		callbacks: newRuntimeCallbacks(scope),
		types:     map[string]bool{},
	}
}

// members defines ingress.event and egress.event.
func (d *definition) members() (javascript.Members[Declarations, Callbacks], error) {
	return javascript.Members[Declarations, Callbacks]{
		Ingress: Some(d.runtime.ToValue(d.ingressRule)),
		Egress:  Some(d.runtime.ToValue(d.egressRule)),
		Declared: func() Declarations {
			return Declarations{Starts: slices.Clone(d.declared.Starts), Produced: slices.Clone(d.declared.Produced)}
		},
		Instance: newCallbacks(d.callbacks),
	}, nil
}

// defineMethod binds owner.method on object, panicking if sobek rejects it.
func (d *definition) defineMethod(object *sobek.Object, owner, method string, fn func(sobek.FunctionCall) sobek.Value) {
	if err := object.Set(method, fn); err != nil {
		panic(d.runtime.NewTypeError("define spectre.%s.%s: %v", owner, method, err))
	}
}

// eventType reads the event type that starts a rule, which no other rule may have.
func (d *definition) eventType(call sobek.FunctionCall, qualified string) string {
	if len(call.Arguments) != 1 {
		panic(d.runtime.NewTypeError("spectre.%s requires an event type", qualified))
	}
	eventType := d.scope.StringArgument(call, 0, qualified, "event type")
	if d.types[eventType] {
		panic(d.runtime.NewTypeError("spectre.%s event type %q already has a rule", qualified, eventType))
	}
	d.types[eventType] = true
	return eventType
}

// ingressRule starts a rule for a reference event type that starts work.
func (d *definition) ingressRule(call sobek.FunctionCall) sobek.Value {
	eventType := d.eventType(call, "ingress.event")
	rule := d.runtime.NewObject()
	d.defineMethod(rule, "IngressEventRule", triggersMethod, func(call sobek.FunctionCall) sobek.Value {
		candidate := eventType
		switch len(call.Arguments) {
		case 0:
		case 1:
			candidate = d.scope.StringArgument(call, 0, "IngressEventRule."+triggersMethod, "event type")
		default:
			panic(d.runtime.NewTypeError("spectre.IngressEventRule.%s takes at most an event type", triggersMethod))
		}
		index := len(d.declared.Starts)
		d.declared.Starts = append(d.declared.Starts, Start{Type: eventType, Candidate: candidate})
		trigger := d.runtime.NewObject()
		d.defineMethod(trigger, "IngressEventTrigger", payloadMethod, func(call sobek.FunctionCall) sobek.Value {
			d.declared.Starts[index].Payload = Some(d.mapping(call, "IngressEventTrigger."+payloadMethod))
			return sobek.Undefined()
		})
		return trigger
	})
	return rule
}

// egressRule starts a rule for a reference event type that work produces.
func (d *definition) egressRule(call sobek.FunctionCall) sobek.Value {
	return d.egressRuleObject(d.eventType(call, "egress.event"), false)
}

// egressRuleObject is an egress rule for eventType whose comparison is not yet declared.
func (d *definition) egressRuleObject(eventType string, optional bool) sobek.Value {
	rule := d.runtime.NewObject()
	d.defineMethod(rule, "EgressEventRule", optionalMethod, func(call sobek.FunctionCall) sobek.Value {
		if len(call.Arguments) != 0 {
			panic(d.runtime.NewTypeError("spectre.EgressEventRule.%s takes no arguments", optionalMethod))
		}
		return d.egressRuleObject(eventType, true)
	})
	d.defineMethod(rule, "EgressEventRule", matchMethod, func(call sobek.FunctionCall) sobek.Value {
		return d.match(call, "EgressEventRule."+matchMethod, eventType, eventType, optional)
	})
	d.defineMethod(rule, "EgressEventRule", pairsMethod, func(call sobek.FunctionCall) sobek.Value {
		const qualified = "EgressEventRule." + pairsMethod
		if len(call.Arguments) != 1 {
			panic(d.runtime.NewTypeError("spectre.%s requires an event type", qualified))
		}
		candidate := d.scope.StringArgument(call, 0, qualified, "event type")
		pairing := d.runtime.NewObject()
		d.defineMethod(pairing, "EgressEventPairing", matchMethod, func(call sobek.FunctionCall) sobek.Value {
			return d.match(call, "EgressEventPairing."+matchMethod, eventType, candidate, optional)
		})
		d.defineMethod(pairing, "EgressEventPairing", payloadMethod, func(call sobek.FunctionCall) sobek.Value {
			mapping := d.mapping(call, "EgressEventPairing."+payloadMethod)
			return d.produce(Produced{Type: eventType, Candidate: candidate, Compared: mapping.To, Payload: Some(mapping)}, optional)
		})
		return pairing
	})
	return rule
}

// match receives the type name compile inserts, and compares payloads as that type unchanged.
func (d *definition) match(call sobek.FunctionCall, qualified, eventType, candidate string, optional bool) sobek.Value {
	if len(call.Arguments) != 1 {
		panic(d.runtime.NewTypeError("spectre.%s requires a type name", qualified))
	}
	compared := d.scope.TypeNameArgument(call, 0, qualified, "type name")
	return d.produce(Produced{Type: eventType, Candidate: candidate, Compared: compared}, optional)
}

// produce declares a produced event type, whose route is the candidate's type, and returns
// the object that maps its result.
func (d *definition) produce(produced Produced, optional bool) sobek.Value {
	d.scope.DeclareEndpoint(javascript.NewEndpoint(model.Egress, Protocol, produced.Candidate, produced.Compared, optional))
	index := len(d.declared.Produced)
	d.declared.Produced = append(d.declared.Produced, produced)
	result := d.runtime.NewObject()
	d.defineMethod(result, "EgressEventResult", resultMethod, func(call sobek.FunctionCall) sobek.Value {
		d.declared.Produced[index].Result = Some(d.mapping(call, "EgressEventResult."+resultMethod))
		return sobek.Undefined()
	})
	return result
}

// mapping receives the two type names compile inserts before a mapping function.
func (d *definition) mapping(call sobek.FunctionCall, qualified string) Mapping {
	if len(call.Arguments) != 3 {
		panic(d.runtime.NewTypeError("spectre.%s requires a function", qualified))
	}
	from := d.scope.TypeNameArgument(call, 0, qualified, "From type name")
	to := d.scope.TypeNameArgument(call, 1, qualified, "To type name")
	function, ok := sobek.AssertFunction(call.Argument(2))
	if !ok {
		panic(d.runtime.NewTypeError("spectre.%s requires a function", qualified))
	}
	return newMapping(d.callbacks.add(function), from, to)
}

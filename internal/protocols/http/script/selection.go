package script

import (
	"context"
	"math"
	"slices"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/schema"
)

// errScriptsNeeded reports that selecting a rule needs a script function, so it needs an evaluator.
var errScriptsNeeded = errors.New("rule selection needs script functions")

// SelectRule returns the first of rules that selects a response, and that the request matches by
// pattern. It starts an evaluator only if a rule needs a script function.
func SelectRule(ctx context.Context, scripts *comparison.Scripts, handle Handle, rules []Rule, request Request, response Response) (Option[Rule], error) {
	selected, err := selectRule(rules, request, response, func() (Callbacks, error) { return newUnavailableCallbacks(), errScriptsNeeded })
	if !errors.Is(err, errScriptsNeeded) {
		return selected, err
	}
	err = scripts.Evaluate(ctx, func(evaluator *javascript.Evaluator) error {
		var err error
		selected, err = selectRule(rules, request, response, func() (Callbacks, error) { return handle.Instance(evaluator), nil })
		return err
	})
	return selected, errors.WithStack(err)
}

// selectRule selects a rule, asking script for the callbacks when a rule needs a function.
func selectRule(rules []Rule, request Request, response Response, script func() (Callbacks, error)) (Option[Rule], error) {
	for _, rule := range rules {
		selected, err := selects(rule, request, response, script)
		if err != nil {
			return None[Rule](), err
		}
		if selected {
			return Some(rule), nil
		}
	}
	return None[Rule](), nil
}

func selects(rule Rule, request Request, response Response, script func() (Callbacks, error)) (bool, error) {
	if _, scripted := rule.Request().(RequestScript); scripted {
		callbacks, err := script()
		if err != nil {
			return false, err
		}
		if selected, err := callbacks.SelectsRequest(rule, request); err != nil || !selected {
			return false, errors.WithStack(err)
		}
	}
	requestHeader := func(callbacks Callbacks, name string, value Option[string]) (bool, error) {
		return errors.WithStack2(callbacks.SelectsRequestHeader(rule, name, value))
	}
	if selected, err := selectsHeaders(rule, rule.RequestHeaders(), request.HeaderValue, requestHeader, script); err != nil || !selected {
		return false, err
	}
	if selected, err := selectsStatus(rule, response.Status, script); err != nil || !selected {
		return false, err
	}
	responseHeader := func(callbacks Callbacks, name string, value Option[string]) (bool, error) {
		return errors.WithStack2(callbacks.SelectsResponseHeader(rule, name, value))
	}
	return selectsHeaders(rule, rule.ResponseHeaders(), response.HeaderValue, responseHeader, script)
}

func selectsStatus(rule Rule, status int, script func() (Callbacks, error)) (bool, error) {
	switch selector := rule.Status().(type) {
	case StatusAny:
		return true, nil
	case StatusCodes:
		return slices.Contains(selector.Codes, status), nil
	case StatusScript:
		callbacks, err := script()
		if err != nil {
			return false, err
		}
		return errors.WithStack2(callbacks.SelectsStatus(rule, status))
	}
	return false, errors.Errorf("rule %s has an unknown status selector", rule)
}

// selectsHeaders reports whether a message satisfies every header selector.
// lookup reads the message's joined header values, and evaluate runs a function.
func selectsHeaders(
	rule Rule,
	headers []HeaderSelector,
	lookup func(name string) Option[string],
	evaluate func(callbacks Callbacks, name string, value Option[string]) (bool, error),
	script func() (Callbacks, error),
) (bool, error) {
	for _, header := range headers {
		switch selector := header.(type) {
		case HeaderValue:
			if !selector.Selects(lookup(selector.Name)) {
				return false, nil
			}
		case HeaderScript:
			callbacks, err := script()
			if err != nil {
				return false, err
			}
			if selected, err := evaluate(callbacks, selector.Name, lookup(selector.Name)); err != nil || !selected {
				return false, err
			}
		default:
			return false, errors.Errorf("rule %s has an unknown header selector", rule)
		}
	}
	return true, nil
}

// NormaliseResponse applies a rule's normaliser to one response. A None status
// or body is not compared, and a returned body must still be the rule's type.
func NormaliseResponse(ctx context.Context, scripts *comparison.Scripts, handle Handle, rule Rule, status int, body Option[schema.Value]) (Option[int], Option[schema.Value], error) {
	match, ok := rule.Action().(ActionMatch)
	if !ok || !match.Normalises {
		return Some(status), body, nil
	}
	code, err := schema.NewNumber(float64(status))
	if err != nil {
		return None[int](), None[schema.Value](), errors.WithStack(err)
	}
	members := map[string]schema.Value{"status": code}
	if value, present := body.Get(); present {
		members["body"] = value
	}
	argument, err := schema.NewObject(members)
	if err != nil {
		return None[int](), None[schema.Value](), errors.WithStack(err)
	}
	result := None[schema.Value]()
	err = scripts.Evaluate(ctx, func(evaluator *javascript.Evaluator) error {
		var err error
		result, err = handle.Instance(evaluator).NormaliseResponse(rule, argument)
		return errors.WithStack(err)
	})
	if err != nil {
		return None[int](), None[schema.Value](), err
	}
	return normalisedResponse(match, result)
}

// normalisedResponse checks a normaliser's result: an object with at most a
// status and a body of the rule's type.
func normalisedResponse(match ActionMatch, result Option[schema.Value]) (Option[int], Option[schema.Value], error) {
	value, _ := result.Get()
	response, ok := value.(schema.ObjectValue)
	if !ok {
		return None[int](), None[schema.Value](), errors.New("response normaliser must return an object")
	}
	status := None[int]()
	body := None[schema.Value]()
	for _, key := range response.Keys() {
		member, _ := response.Member(key)
		switch key {
		case "status":
			number, isNumber := member.(schema.NumberValue)
			code := number.Float()
			if !isNumber || code != math.Trunc(code) || code < 100 || code > 599 {
				return None[int](), None[schema.Value](), errors.New("normalised status must be an integer from 100 to 599")
			}
			status = Some(int(code))
		case "body":
			body = Some(member)
		default:
			return None[int](), None[schema.Value](), errors.Errorf("normalised response has an unknown member %q", key)
		}
	}
	member, present := body.Get()
	if !present {
		return status, None[schema.Value](), nil
	}
	checked, err := schema.As(match.Type, member)
	if err != nil {
		return None[int](), None[schema.Value](), errors.Wrap(err, "validate normalised body")
	}
	return status, Some(checked), nil
}

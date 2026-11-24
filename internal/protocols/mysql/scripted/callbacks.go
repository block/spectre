package scripted

import (
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/comparison/javascript"
)

// Claim returns the rows of the reference statement with values that a combining
// replacement's using function claims from one of its sources.
type Claim func(values []Option[string]) ([]Row, error)

// Offer receives each reply that a splitting replacement's using function offers for
// the candidate statement at index, with its values.
type Offer func(index int, values []Option[string], rows []Row) error

// runtimeCallbacks are one runtime's using functions, indexed like the declared replacements.
type runtimeCallbacks struct {
	scope *javascript.Scope
	users []sobek.Callable
}

func newRuntimeCallbacks(scope *javascript.Scope) *runtimeCallbacks {
	return &runtimeCallbacks{scope: scope}
}

// Callbacks are the using functions of the statement replacements in one evaluator's runtime, so they
// are confined to its caller. The zero value stands for an evaluator that was not built with the binding.
type Callbacks struct {
	runtime Option[*runtimeCallbacks]
}

func newCallbacks(runtime *runtimeCallbacks) Callbacks {
	return Callbacks{runtime: Some(runtime)}
}

// user returns the using function of the replacement, and the scope that runs it.
func (c Callbacks) user(replacement Replacement) (sobek.Callable, *javascript.Scope, bool) {
	runtime, ok := c.runtime.Get()
	if !ok || replacement.Index() < 0 || replacement.Index() >= len(runtime.users) {
		return nil, nil, false
	}
	return runtime.users[replacement.Index()], runtime.scope, true
}

// Combine answers a candidate statement with the rows a combining replacement's using
// function builds from claims, one per source. Time waiting in claims is not counted against the timeout.
func (c Callbacks) Combine(replacement Replacement, values []Option[string], claims []Claim) ([]Row, error) {
	using, scope, ok := c.user(replacement)
	if !ok {
		return nil, errors.Errorf("JavaScript function of %s is unavailable", replacement)
	}
	runtime := scope.Runtime()
	failure := None[error]()
	functions := make([]sobek.Value, 0, len(claims))
	for _, claim := range claims {
		functions = append(functions, runtime.ToValue(func(call sobek.FunctionCall) sobek.Value {
			claimed, err := statementValues(call.Arguments)
			var rows []Row
			if err == nil {
				err = scope.Wait(func() error {
					var claimErr error
					rows, claimErr = claim(claimed)
					return claimErr
				})
			}
			if err != nil {
				failure = Some(err)
				panic(runtime.NewGoError(err))
			}
			return rowsValue(runtime, rows)
		}))
	}
	result, err := scope.Invoke(using, threw(replacement), valuesValue(runtime, values), arrayValue(runtime, functions))
	if failed, claimFailed := failure.Get(); claimFailed {
		return nil, errors.WithStack(failed)
	}
	if err != nil {
		return nil, errors.WithStack(err)
	}
	rows, err := readRows(result)
	return rows, errors.Wrapf(err, "result of %s", replacement)
}

// Split passes the replies a splitting replacement's using function offers for
// a reference statement's values and rows to offer.
func (c Callbacks) Split(replacement Replacement, values []Option[string], rows []Row, offer Offer) error {
	using, scope, ok := c.user(replacement)
	if !ok {
		return errors.Errorf("JavaScript function of %s is unavailable", replacement)
	}
	runtime := scope.Runtime()
	failure := None[error]()
	functions := make([]sobek.Value, 0, len(replacement.Statements()))
	for index := range replacement.Statements() {
		functions = append(functions, runtime.ToValue(func(call sobek.FunctionCall) sobek.Value {
			var offered []Option[string]
			var offeredRows []Row
			err := errors.New("offer requires rows after its values")
			if last := len(call.Arguments) - 1; last >= 0 {
				offered, err = statementValues(call.Arguments[:last])
				if err == nil {
					offeredRows, err = readRows(call.Arguments[last])
				}
			}
			if err == nil {
				err = offer(index, offered, offeredRows)
			}
			if err != nil {
				failure = Some(err)
				panic(runtime.NewGoError(err))
			}
			return sobek.Undefined()
		}))
	}
	_, err := scope.Invoke(using, threw(replacement), valuesValue(runtime, values), rowsValue(runtime, rows), arrayValue(runtime, functions))
	if failed, offerFailed := failure.Get(); offerFailed {
		return errors.WithStack(failed)
	}
	return errors.WithStack(err)
}

// threw is the message of an exception in a replacement's using function.
func threw(replacement Replacement) string {
	return "JavaScript function of " + replacement.String() + " threw an exception"
}

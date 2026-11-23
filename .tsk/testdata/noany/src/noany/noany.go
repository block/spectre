package noany

import (
	"encoding/json"

	"ext"
)

var value any // want `replace the empty interface`

var empty interface{} // want `replace the empty interface`

var values map[string]any // want `replace the empty interface`

func Take(v any) {} // want `replace the empty interface`

func Generic[T any](v T) T { return v }

type Box[T any] struct{ value T }

type Stringer interface{ String() string }

type Number interface{ ~int | ~float64 }

func Sum[T Number](values []T) (total T) {
	for _, v := range values {
		total += v
	}
	return total
}

func Allowed(v any) {} //nolint:noany // implements an external interface

func Shadowed() {
	type any = int
	var x any
	_ = x
}

type handler struct{}

func (handler) Handle(value any) error { return nil }

func (handler) handle(value any) error { return nil }

// The signature differs from ext.Handler.Handle.
type wrongResult struct{}

func (wrongResult) Handle(value any) {} // want `replace the empty interface`

// Only part of ext.Pair is implemented.
type half struct{}

func (half) First(value any) {} // want `replace the empty interface`

// The interface is local, so its own declaration is the root to fix.
type local interface {
	Visit(value any) // want `replace the empty interface`
}

type visitor struct{}

func (visitor) Visit(value any) {} // want `replace the empty interface`

func Passed() {
	ext.Register(func(value any) error {
		var held any // want `replace the empty interface`
		_ = held
		return nil
	})
	ext.Register(handler{}.handle)
	ext.Take(func(value any) {})
	_ = ext.Pool{New: func() any { return nil }}
	var pool ext.Pool
	pool.New = func() any { return nil }
	ext.Each([]int{}, func(value int) {})
	ext.Each([]any{}, func(value any) {}) // want `replace the empty interface` `replace the empty interface`
	register(func(value any) error { return nil }) // want `replace the empty interface`
	ext.Register(declared)
	callLocal(unused)
}

func register(handle func(value any) error) {} // want `replace the empty interface`

func declared(value any) error { return nil }

func unused(value any) error { return nil } // want `replace the empty interface`

func callLocal(handle func(value any) error) {} // want `replace the empty interface`

func forward(format string, args ...any) {
	ext.Printf(format, args...)
}

func forwardLocal(format string, args ...any) { // want `replace the empty interface`
	forward(format, args...)
}

func inspected(format string, args ...any) { // want `replace the empty interface`
	_ = len(args)
	ext.Printf(format, args...)
}

func unspread(args ...any) { // want `replace the empty interface`
	ext.Print(args)
}

func ignored(args ...any) {} // want `replace the empty interface`

var raw json.RawMessage // want `instead of json.RawMessage`

var raws map[string]json.RawMessage // want `instead of json.RawMessage`

type rawAlias = json.RawMessage // want `instead of json.RawMessage`

var aliased rawAlias // want `instead of json.RawMessage`

func converted(data []byte) {
	_ = json.RawMessage(data) // want `instead of json.RawMessage`
}

func decodedRaw(data []byte) {
	ext.Decode(data, func(value json.RawMessage) {})
}

type message struct{ value []byte }

package ext

import "encoding/json"

type Handler interface {
	Handle(value any) error
}

type Pair interface {
	First(value any)
	Second()
}

type Callback func(value any)

type Pool struct {
	New func() any
}

func Register(handle func(value any) error) {}

func Take(callback Callback) {}

func Each[T any](values []T, visit func(value T)) {}

func Printf(format string, args ...any) {}

func Print(args []any) {}

func Decode(data []byte, visit func(value json.RawMessage)) {}

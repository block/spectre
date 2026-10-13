package optional

type Option[T any] struct{ value T }

func Some[T any](value T) Option[T] { return Option[T]{value: value} }

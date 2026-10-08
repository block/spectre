package optional

type Option[T any] struct {
	value T
	ok    bool
}

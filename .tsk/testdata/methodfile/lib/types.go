package lib

type Thing struct{}

func (t Thing) Same() {}

type Box[T any] struct{ value T }

type Other int

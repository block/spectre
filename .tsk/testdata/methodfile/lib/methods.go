package lib

func (t *Thing) Elsewhere() {} // want `Thing.Elsewhere must be declared in types.go with its type`

func (b Box[T]) Get() T { return b.value } // want `Box.Get must be declared in types.go with its type`

func (o Other) Kept() {} //nolint:methodfile kept for the test

type Local struct{}

func (l Local) Here() {}

func Function() {}

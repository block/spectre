package public

import (
	"context"

	"example.com/m/internal/lib"
)

type Alias = lib.Thing // want `Alias exposes internal type example.com/m/internal/lib.Thing; mirror it in this package`

type Embeds struct { // want `Embeds exposes internal type example.com/m/internal/lib.Thing; mirror it in this package`
	lib.Thing
}

type Field struct { // want `Field exposes internal type example.com/m/internal/lib.Name; mirror it in this package`
	Name lib.Name
}

type Method interface { // want `Method exposes internal type example.com/m/internal/lib.Thing; mirror it in this package`
	Load(ctx context.Context) (lib.Thing, error)
}

type Generic[T lib.Source] struct{ Value T } // want `Generic exposes internal type example.com/m/internal/lib.Source; mirror it in this package`

type Receiver struct{}

func (Receiver) Thing() *lib.Thing { return nil } // want `Receiver.Thing exposes internal type example.com/m/internal/lib.Thing; mirror it in this package`

func Result() []lib.Thing { return nil } // want `Result exposes internal type example.com/m/internal/lib.Thing; mirror it in this package`

func Param(things map[string]lib.Name) {} // want `Param exposes internal type example.com/m/internal/lib.Name; mirror it in this package`

func Instance() lib.Box[string] { return lib.Box[string]{} } // want `Instance exposes internal type example.com/m/internal/lib.Box\[string\]; mirror it in this package`

func Argument() Wrapper[lib.Name] { return Wrapper[lib.Name]{} } // want `Argument exposes internal type example.com/m/internal/lib.Name; mirror it in this package`

type EmbedsHidden struct { // want `EmbedsHidden exposes internal type example.com/m/internal/lib.Thing; mirror it in this package`
	hidden
}

type EmbedsPromoted struct {
	promoted
}

func Promoted() promoted { return promoted{} } // want `Promoted exposes internal type example.com/m/internal/lib.Thing; mirror it in this package`

func Head() *node { return nil } // want `Head exposes internal type example.com/m/internal/lib.Name; mirror it in this package`

var Value = lib.New() // want `Value exposes internal type example.com/m/internal/lib.Thing; mirror it in this package`

// Mirror is a public copy of lib.Thing, converted where it crosses the boundary.
type Mirror struct {
	Name string

	thing lib.Thing
}

func (m Mirror) thingValue() lib.Thing { return m.thing }

type Wrapper[T any] struct{ Value T }

func Convert(mirror Mirror) Mirror { return Mirror{Name: lib.New().Name} }

func Context(ctx context.Context) error { return nil }

type hidden struct{ Thing lib.Thing }

// promoted's exported method is promoted into any exported type embedding it.
type promoted struct{}

func (promoted) Thing() lib.Thing { return lib.New() } // want `EmbedsPromoted.Thing exposes internal type example.com/m/internal/lib.Thing; mirror it in this package`

func (promoted) thing() lib.Thing { return lib.New() }

// node refers to itself, which the walk must not follow forever.
type node struct {
	Next  *node
	Value lib.Name
}

// Recursive's constraint refers to its own type parameter.
type Recursive[T interface{ ~[]T }] []T

func unexported() lib.Thing { return lib.New() }

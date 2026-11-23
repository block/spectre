package payload

import (
	"dynamic"
	"ext"
)

var value dynamic.Value

var values map[string]dynamic.Value

var other dynamic.Other // want `Other is an empty interface`

var defined dynamic.Defined // want `Defined is an empty interface`

var external ext.Value // want `Value is an empty interface`

// A configured package may alias the type, but each use of the alias is reported.
type local = dynamic.Value

var aliased local // want `local is an empty interface`

func Generic[T any](value T) T { return value }

func Comparable[T comparable](value T) T { return value }

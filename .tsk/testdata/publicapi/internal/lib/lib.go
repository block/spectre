package lib

type Thing struct{ Name string }

type Name string

type Source interface{ Load() (Thing, error) }

type Box[T any] struct{ Value T }

// Internal packages may expose each other's types.
func New() Thing { return Thing{} }

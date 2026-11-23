package dynamic

// Value is typed only at runtime.
type Value = any

// Other is not configured.
type Other = any // want `replace the empty interface`

// Defined is a configured name, but not an alias.
type Defined any // want `replace the empty interface`

var own Value

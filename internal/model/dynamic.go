package model

// DynamicValue is a value typed only at runtime, such as a payload its TypeScript type
// describes. It aliases any, so decoded JSON and type switches over it work unchanged.
type DynamicValue = any

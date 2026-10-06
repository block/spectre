// Package jsonschema generates TypeScript schema declarations from JSON Schema.
package jsonschema

import (
	"github.com/alecthomas/errors"
)

// Config selects the JSON Schema files declared as one TypeScript module.
type Config struct {
	// Module names the declared module, which scripts import the types from.
	Module string `required:"" placeholder:"NAME" help:"Name of the TypeScript module to declare the types in."`
	// Schemas are JSON Schema files whose titled roots and definitions become types.
	Schemas []string `arg:"" name:"schema" type:"existingfile" help:"JSON Schema files. Each titled root schema and each $defs or definitions entry becomes a type."`
}

// Validate checks that the configuration names a module and at least one schema.
func (c Config) Validate() error {
	if c.Module == "" {
		return errors.New("module name is required")
	}
	if len(c.Schemas) == 0 {
		return errors.New("at least one JSON Schema file is required")
	}
	return nil
}

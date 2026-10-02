package schema

import (
	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
)

// Config selects the TypeScript declarations that make up the schema.
type Config struct {
	// SchemaDir holds TypeScript declaration files, each declaring ambient modules.
	SchemaDir string `required:"" type:"existingdir" placeholder:"DIR" help:"Directory of TypeScript declarations typing every payload. Every .ts file in it, including subdirectories, declares modules with declare module \"name\" { ... }."`
}

// NewConfig returns the default schema configuration.
func NewConfig() Config {
	// ApplyDefaults validates required fields, so seed the directory while applying tag defaults.
	config := Config{SchemaDir: "placeholder"}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply schema defaults"))
	}
	config.SchemaDir = ""
	return config
}

// Validate checks that the schema configuration is usable.
func (c Config) Validate() error {
	if c.SchemaDir == "" {
		return errors.New("schema directory is required")
	}
	return nil
}

package comparison

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
)

// Config contains the command-line configuration for response comparison.
type Config struct {
	// ScriptsDir holds the normaliser scripts, which are all loaded as one set.
	ScriptsDir string `required:"" type:"existingdir" help:"Directory of normaliser scripts. Every .js file in it, including subdirectories, is loaded."`
	// ComparisonTimeout limits one response comparison.
	ComparisonTimeout time.Duration `default:"1s" help:"Maximum duration of one response comparison."`
	// ComparisonMaxResponseBytes limits each captured backend response body.
	ComparisonMaxResponseBytes int `default:"1048576" help:"Maximum captured response bytes per backend."`
}

// NewConfig returns the default response comparison configuration.
func NewConfig() Config {
	// ApplyDefaults validates required fields, so seed the directory while applying tag defaults.
	config := Config{ScriptsDir: "placeholder"}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply comparison defaults"))
	}
	config.ScriptsDir = ""
	return config
}

// Validate checks that the comparison configuration is usable.
func (c Config) Validate() error {
	if c.ScriptsDir == "" {
		return errors.New("scripts directory is required")
	}
	if c.ComparisonTimeout <= 0 {
		return errors.New("comparison timeout must be positive")
	}
	if c.ComparisonMaxResponseBytes <= 0 {
		return errors.New("comparison maximum response size must be positive")
	}
	return nil
}

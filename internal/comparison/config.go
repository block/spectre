package comparison

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
)

// Config contains the command-line configuration for response comparison.
type Config struct {
	// ComparisonScript is the JavaScript comparator file.
	ComparisonScript string `required:"" type:"existingfile" help:"JavaScript response comparator file."`
	// ComparisonTimeout limits one response comparison.
	ComparisonTimeout time.Duration `default:"1s" help:"Maximum duration of one response comparison."`
	// ComparisonMaxResponseBytes limits each captured backend response body.
	ComparisonMaxResponseBytes int `default:"1048576" help:"Maximum captured response bytes per backend."`
}

// NewConfig returns the default response comparison configuration.
func NewConfig() Config {
	// ApplyDefaults validates required fields, so seed the script while applying tag defaults.
	config := Config{ComparisonScript: "placeholder"}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply comparison defaults"))
	}
	config.ComparisonScript = ""
	return config
}

// Validate checks that the comparison configuration is usable.
func (c Config) Validate() error {
	if c.ComparisonScript == "" {
		return errors.New("comparison script is required")
	}
	if c.ComparisonTimeout <= 0 {
		return errors.New("comparison timeout must be positive")
	}
	if c.ComparisonMaxResponseBytes <= 0 {
		return errors.New("comparison maximum response size must be positive")
	}
	return nil
}

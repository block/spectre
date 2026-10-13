package protocol

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/schema"
)

// Config contains the command-line configuration for pairing and comparison.
type Config struct {
	// Comparison configures the scripts that normalise, compare, and hash payloads.
	Comparison comparison.Config `embed:""`
	// MatchWindow bounds how long an unused reference stays available, and how
	// long a candidate waits for one.
	MatchWindow time.Duration `default:"5s" help:"How long reference and candidate requests wait to be paired."`
}

// NewConfig returns the default engine configuration.
func NewConfig() Config {
	// ApplyDefaults validates required fields, so seed the directories while applying tag defaults.
	config := Config{Comparison: comparison.Config{ScriptsDir: "placeholder", Schema: schema.Config{SchemaDir: "placeholder"}}}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply engine defaults"))
	}
	config.Comparison.ScriptsDir = ""
	config.Comparison.Schema.SchemaDir = ""
	return config
}

// Validate checks that the engine configuration is usable.
func (c Config) Validate() error {
	if err := c.Comparison.Validate(); err != nil {
		return errors.WithStack(err)
	}
	if c.MatchWindow <= 0 {
		return errors.New("match window must be positive")
	}
	return nil
}

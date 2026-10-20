// Package logger configures structured logging and carries loggers through contexts.
package logger

import (
	"context"
	"io"
	"log/slog"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
	"github.com/lmittmann/tint"
)

// Config contains logging flags that can be embedded in a Kong CLI.
type Config struct {
	// Level is the minimum severity written to the log.
	Level slog.Level `default:"info" help:"Minimum log level (debug, info, warn, error)."`
	// JSON shapes JSON output like the fronted service's logs. Unset writes text.
	JSON JSONTemplate `placeholder:"TEMPLATE" help:"Write logs as JSON shaped by this template, so they match the service SPECTRE stands in for. DEFAULT uses '{\"time\": \"<time>\", \"level\": \"<level>\", \"msg\": \"<msg>\"}'. Placeholders are <msg>, <level> or <level:LEVEL=NAME,...>, <time> or <time:FORMAT>, and <attrs>; without <attrs>, attributes go at the top level."`
}

// NewConfig returns the default logging configuration: info-level text output.
func NewConfig() Config {
	config := Config{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply logger defaults"))
	}
	return config
}

// New constructs a logger that writes to output using the supplied configuration.
func New(config Config, output io.Writer) *slog.Logger {
	if config.JSON.enabled() {
		return slog.New(newTemplateHandler(&config.JSON, config.Level, output))
	}
	return slog.New(tint.NewTextHandler(output, &tint.Options{
		Level: config.Level,
		ReplaceAttr: func(groups []string, attribute slog.Attr) slog.Attr {
			// Process supervisors add a timestamp to text logs; omit a competing one here.
			if len(groups) == 0 && attribute.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attribute
		},
	}))
}

// Event names for the notable proxy events, used as the event tag value.
const (
	EventQuarantine  = "quarantine"
	EventReceived    = "received"
	EventCorrelation = "correlation"
)

// WithLogger returns a child context carrying log without changing the parent context.
func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, newContextKey(), log)
}

// FromContext returns the attached logger, or a logger that discards output if none is attached.
func FromContext(ctx context.Context) *slog.Logger {
	log, ok := ctx.Value(newContextKey()).(*slog.Logger)
	if !ok || log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return log
}

// Values of this private type compare equal, so context lookups need no global key.
type contextKey struct{}

func newContextKey() contextKey {
	return contextKey{}
}

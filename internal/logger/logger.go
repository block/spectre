// Package logger configures structured logging and carries loggers through contexts.
package logger

import (
	"context"
	"io"
	"log/slog"

	"github.com/lmittmann/tint"
)

// Config contains logging flags that can be embedded in a Kong CLI.
type Config struct {
	// Level is the minimum severity written to the log.
	Level slog.Level `name:"log-level" default:"info" help:"Minimum log level (debug, info, warn, error)."`
	// JSON enables JSON output instead of text output.
	JSON bool `name:"log-json" help:"Write logs as JSON."`
}

// NewConfig returns the default logging configuration: info-level text output.
func NewConfig() Config {
	return Config{Level: slog.LevelInfo}
}

// New constructs a logger that writes to output using the supplied configuration.
func New(config Config, output io.Writer) *slog.Logger {
	if config.JSON {
		return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: config.Level}))
	}
	return slog.New(tint.NewTextHandler(output, &tint.Options{Level: config.Level}))
}

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

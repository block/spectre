package http

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/httpservice"
)

// ServerConfig contains the configuration shared by ingress and egress.
type ServerConfig struct {
	// TransportConfig holds the limits shared with the backend transport.
	httpservice.TransportConfig `embed:""`
	// MaxConnections limits concurrent client connections.
	MaxConnections int `default:"512" help:"Maximum concurrent client connections."`
	// ReadHeaderTimeout limits the duration spent reading request headers.
	ReadHeaderTimeout time.Duration `default:"10s" help:"Maximum duration for reading request headers."`
	// IdleTimeout limits how long an idle client connection remains open.
	IdleTimeout time.Duration `default:"1m" help:"Maximum idle client connection duration."`
	// ShutdownTimeout limits graceful server shutdown.
	ShutdownTimeout time.Duration `default:"10s" help:"Maximum graceful shutdown duration."`
	// ComparisonMaxBodyBytes limits each captured request or response body.
	ComparisonMaxBodyBytes int `default:"1048576" help:"Maximum captured body bytes per request or response."`
	// Descriptors supplies static protobuf descriptors. Ingress adds any reflected ones.
	Descriptors descriptors.Config `embed:""`
}

// NewServerConfig returns the default server configuration.
func NewServerConfig() ServerConfig {
	config := ServerConfig{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply server defaults"))
	}
	return config
}

// ServeConfig returns the limits the serve loop applies to each server.
func (c ServerConfig) ServeConfig() httpservice.ServeConfig {
	return httpservice.ServeConfig{
		MaxConnections:    c.MaxConnections,
		MaxHeaderBytes:    c.MaxHeaderBytes,
		ReadHeaderTimeout: c.ReadHeaderTimeout,
		IdleTimeout:       c.IdleTimeout,
		ShutdownTimeout:   c.ShutdownTimeout,
	}
}

// Validate checks that every limit is positive.
func (c ServerConfig) Validate() error {
	if err := requirePositive(
		limit[time.Duration]{name: "read header timeout", value: c.ReadHeaderTimeout},
		limit[time.Duration]{name: "idle timeout", value: c.IdleTimeout},
		limit[time.Duration]{name: "shutdown timeout", value: c.ShutdownTimeout},
	); err != nil {
		return err
	}
	return requirePositive(
		limit[int]{name: "maximum in-flight requests", value: c.MaxInFlightRequests},
		limit[int]{name: "maximum connections", value: c.MaxConnections},
		limit[int]{name: "maximum header size", value: c.MaxHeaderBytes},
		limit[int]{name: "comparison maximum body size", value: c.ComparisonMaxBodyBytes},
	)
}

// limit is a named configuration value that must be positive.
type limit[T int | time.Duration] struct {
	name  string
	value T
}

// requirePositive reports the first limit that is not positive.
func requirePositive[T int | time.Duration](limits ...limit[T]) error {
	for _, checked := range limits {
		if checked.value <= 0 {
			return errors.Errorf("%s must be positive", checked.name)
		}
	}
	return nil
}

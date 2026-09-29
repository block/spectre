// Package proxy holds the forwarding, admission, and serving code shared by proxies.
package proxy

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
)

// Config contains the server and forwarding limits shared by every proxy.
type Config struct {
	// MaxInFlightRequests limits concurrent proxied requests.
	MaxInFlightRequests int `default:"256" help:"Maximum concurrent proxied requests."`
	// MaxConnections limits concurrent client connections.
	MaxConnections int `default:"512" help:"Maximum concurrent client connections."`
	// ReadHeaderTimeout limits the duration spent reading request headers.
	ReadHeaderTimeout time.Duration `default:"10s" help:"Maximum duration for reading request headers."`
	// IdleTimeout limits how long an idle client connection remains open.
	IdleTimeout time.Duration `default:"1m" help:"Maximum idle client connection duration."`
	// MaxHeaderBytes limits the size of request and response headers.
	MaxHeaderBytes int `default:"65536" help:"Maximum request header size in bytes."`
	// ShutdownTimeout limits graceful server shutdown.
	ShutdownTimeout time.Duration `default:"10s" help:"Maximum graceful shutdown duration."`
}

// NewConfig returns the default proxy configuration.
func NewConfig() Config {
	config := Config{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply proxy defaults"))
	}
	return config
}

// Validate checks that every limit is positive.
func (c Config) Validate() error {
	positiveDurations := []struct {
		name  string
		value time.Duration
	}{
		{name: "read header timeout", value: c.ReadHeaderTimeout},
		{name: "idle timeout", value: c.IdleTimeout},
		{name: "shutdown timeout", value: c.ShutdownTimeout},
	}
	for _, duration := range positiveDurations {
		if duration.value <= 0 {
			return errors.Errorf("%s must be positive", duration.name)
		}
	}
	positiveSizes := []struct {
		name  string
		value int
	}{
		{name: "maximum in-flight requests", value: c.MaxInFlightRequests},
		{name: "maximum connections", value: c.MaxConnections},
		{name: "maximum header size", value: c.MaxHeaderBytes},
	}
	for _, size := range positiveSizes {
		if size.value <= 0 {
			return errors.Errorf("%s must be positive", size.name)
		}
	}
	return nil
}

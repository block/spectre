package httpservice

import (
	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
)

// TransportConfig contains the limits the backend transport shares with the servers.
type TransportConfig struct {
	// MaxInFlightRequests limits concurrent proxied requests.
	MaxInFlightRequests int `default:"256" help:"Maximum concurrent proxied requests."`
	// MaxHeaderBytes limits the size of request and response headers.
	MaxHeaderBytes int `default:"65536" help:"Maximum request header size in bytes."`
}

// NewTransportConfig returns the default transport configuration.
func NewTransportConfig() TransportConfig {
	config := TransportConfig{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply transport defaults"))
	}
	return config
}

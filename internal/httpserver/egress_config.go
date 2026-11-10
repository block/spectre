package httpserver

import (
	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
)

// EgressConfig contains the command-line configuration for an egress proxy.
type EgressConfig struct {
	// ReferenceListen receives reference traffic, which is always forwarded.
	ReferenceListen string `default:"127.0.0.1:50060" help:"Reference listener address: host:port, or unix:<path|@abstract> for a unix socket."`
	// CandidateListen receives candidate traffic. It must stay local: a loopback
	// IP or a unix socket.
	CandidateListen string `default:"127.0.0.1:50061" help:"Candidate listener address on a loopback IP or unix socket: host:port, or unix:<path|@abstract>."`
	// HealthListen serves liveness and readiness. Probes need their own listener,
	// because any path on the traffic listeners may belong to a dependency.
	HealthListen string `default:"127.0.0.1:50062" help:"Health listener address for /livez and /readyz: host:port, or unix:<path|@abstract>."`
	// Destinations maps each request host to its upstream URL, using the backend
	// URL forms that ingress accepts.
	Destinations map[string]string `name:"destination" placeholder:"HOST=URL" help:"Upstream URL for requests to HOST: http, https, h2c, http+unix:<socket>, or h2c+unix:<socket>. HOST may be a glob, such as *.example. Repeat for each dependency."`
	// FallbackDestination receives reference requests to hosts no destination matches.
	FallbackDestination string `placeholder:"URL" help:"Upstream URL for reference requests to hosts no destination matches, in any form --destination accepts."`
	// RecordingBufferBytes limits recorded reference responses across all requests.
	RecordingBufferBytes int `default:"33554432" help:"Maximum reference response bytes recorded across all requests."`
	// Config holds the configuration shared with ingress.
	Config `embed:""`
}

// requiredPlaceholder seeds required fields while ApplyDefaults validates a config.
const requiredPlaceholder = "placeholder"

// NewEgressConfig returns the default egress configuration.
func NewEgressConfig() EgressConfig {
	// ApplyDefaults validates required fields, so seed them while applying tag defaults.
	config := EgressConfig{FallbackDestination: requiredPlaceholder}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply egress defaults"))
	}
	config.FallbackDestination = ""
	return config
}

// Validate checks that the egress has a fallback destination and usable resource limits.
func (c EgressConfig) Validate() error {
	if c.FallbackDestination == "" {
		return errors.New("fallback destination is required")
	}
	if err := requirePositive(limit[int]{name: "recording buffer size", value: c.RecordingBufferBytes}); err != nil {
		return err
	}
	return errors.WithStack(c.Config.Validate())
}

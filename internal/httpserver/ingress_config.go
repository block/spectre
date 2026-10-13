package httpserver

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/netaddr"
)

// IngressConfig contains the command-line configuration for an ingress proxy.
type IngressConfig struct {
	// Listen is the address for the ingress HTTP server. A "unix:" prefix binds a
	// unix socket, either an absolute path or an abstract name beginning with "@".
	Listen string `default:"127.0.0.1:50050" help:"Ingress server address: host:port, or unix:<path|@abstract> for a unix socket."`
	// Reference is the authoritative backend URL, using http, https, or h2c over
	// TCP, or http+unix/h2c+unix over a unix socket.
	Reference string `required:"" help:"Reference backend URL: http, https, h2c, or http+unix:<socket> / h2c+unix:<socket>."`
	// Candidate is the mirrored backend URL. It must stay local: a loopback IP or
	// a unix socket, using http, https, or h2c.
	Candidate string `required:"" help:"Candidate backend URL on a loopback IP or unix socket: http, https, h2c, http+unix, or h2c+unix."`
	// CandidateTimeout limits the duration of a candidate request.
	CandidateTimeout time.Duration `default:"30s" help:"Maximum duration of a candidate request."`
	// Reflection loads and compares both backends' descriptors at startup.
	Reflection bool `default:"true" negatable:"" help:"Load backend descriptors with gRPC reflection and require them to match."`
	// ReflectionTimeout limits the startup descriptor comparison.
	ReflectionTimeout time.Duration `default:"10s" help:"Maximum duration of the startup gRPC reflection check."`
	// CandidateBufferBytes limits queued request data across all candidates.
	CandidateBufferBytes int `default:"1048576" help:"Maximum request bytes buffered across candidates before quarantine."`
	// Config holds the configuration shared with egress.
	Config `embed:""`
}

// NewIngressConfig returns the default ingress configuration.
func NewIngressConfig() IngressConfig {
	// ApplyDefaults validates required fields, so seed them while applying tag defaults.
	config := IngressConfig{Reference: "placeholder", Candidate: "placeholder"}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply ingress defaults"))
	}
	config.Reference = ""
	config.Candidate = ""
	return config
}

// ListenNetworkAddress reports the network and address for the ingress listener.
func (c IngressConfig) ListenNetworkAddress() (network string, address string) {
	endpoint := netaddr.ParseListen(c.Listen)
	return endpoint.Network(), endpoint.Address()
}

// Validate checks that the ingress has usable resource limits.
func (c IngressConfig) Validate() error {
	if err := requirePositive(
		limit[time.Duration]{name: "candidate timeout", value: c.CandidateTimeout},
		limit[time.Duration]{name: "reflection timeout", value: c.ReflectionTimeout},
	); err != nil {
		return err
	}
	if err := requirePositive(limit[int]{name: "candidate buffer size", value: c.CandidateBufferBytes}); err != nil {
		return err
	}
	return errors.WithStack(c.Config.Validate())
}

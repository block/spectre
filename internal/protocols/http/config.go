// Package http serves HTTP ingress and egress points for the protocol engine.
// Both share one handler that forwards, records, replays, and reports as the engine decides.
package http

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/descriptors"
)

// requiredPlaceholder fills a required field while Kong applies the defaults, which fails on one left empty.
const requiredPlaceholder = "placeholder"

// Config contains the command-line configuration of the HTTP proxies. The limits
// that the flags leave out are the defaults of IngressConfig and EgressConfig.
type Config struct {
	// Listen is the public ingress address that clients send traffic to.
	Listen string `default:"127.0.0.1:50050" help:"Public ingress address: host:port, or unix:<path|@abstract> for a unix socket."`
	// ReferenceURL is where ingress reaches the reference service.
	ReferenceURL string `name:"reference-url" required:"" help:"Reference service URL: http, https, h2c, or http+unix:<socket> / h2c+unix:<socket>."`
	// CandidateURL is where ingress reaches the candidate service.
	CandidateURL string `name:"candidate-url" required:"" help:"Candidate service URL on a loopback IP or unix socket: http, https, h2c, http+unix, or h2c+unix."`
	// ReferenceEgressListen receives the reference's dependency calls.
	ReferenceEgressListen string `default:"127.0.0.1:50060" help:"Address the reference sends its dependency calls to: host:port, or unix:<path|@abstract>."`
	// CandidateEgressListen receives the candidate's dependency calls.
	CandidateEgressListen string `default:"127.0.0.1:50061" help:"Address the candidate sends its dependency calls to, on a loopback IP or unix socket: host:port, or unix:<path|@abstract>."`
	// Reflection loads and compares both backends' descriptors via gRPC reflection.
	Reflection bool `name:"reflection" negatable:"" default:"true" help:"Load backend descriptors with gRPC reflection and require them to match; disable to compare using only the static descriptors. Ingress is ready only once both backends accept."`
	// Destinations maps each request host to its upstream URL for egress.
	Destinations map[string]string `name:"destination" placeholder:"HOST=URL" help:"Upstream URL for requests to HOST, passed to the egress proxy. HOST may be a glob, such as *.example. Repeat for each dependency."`
	// CandidateStartupTimeout bounds how long the candidate has to accept a first connection.
	CandidateStartupTimeout time.Duration `default:"5m" help:"Maximum time for the candidate to accept a first connection before it is quarantined. Ingress sends it nothing until then."`
	// Descriptors supplies protobuf descriptors to both proxies.
	Descriptors descriptors.Config `embed:""`
}

// NewConfig returns the default HTTP configuration, without the backends it requires.
func NewConfig() Config {
	// ApplyDefaults validates required fields, so seed them while applying tag defaults.
	config := Config{ReferenceURL: requiredPlaceholder, CandidateURL: requiredPlaceholder}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply HTTP defaults"))
	}
	config.ReferenceURL = ""
	config.CandidateURL = ""
	return config
}

// Validate checks the timeout that only the HTTP flags set. The proxies check their own limits.
func (c Config) Validate() error {
	if c.CandidateStartupTimeout <= 0 {
		return errors.New("candidate startup timeout must be positive")
	}
	return nil
}

// ingressConfig builds the ingress proxy configuration.
func (c Config) ingressConfig() IngressConfig {
	config := NewIngressConfig()
	config.Listen = c.Listen
	config.Reference = c.ReferenceURL
	config.Candidate = c.CandidateURL
	config.Reflection = c.Reflection
	config.CandidateStartupTimeout = c.CandidateStartupTimeout
	config.Descriptors = c.Descriptors
	return config
}

// egressConfig builds the egress proxy configuration.
func (c Config) egressConfig() EgressConfig {
	config := NewEgressConfig()
	config.ReferenceListen = c.ReferenceEgressListen
	config.CandidateListen = c.CandidateEgressListen
	config.Destinations = c.Destinations
	config.Descriptors = c.Descriptors
	return config
}

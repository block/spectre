package mirror

import (
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// ProxyConfig contains the command-line configuration of a mirror whose services
// run elsewhere, such as in other containers of the same pod.
type ProxyConfig struct {
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
	// EgressHealthListen serves the egress proxy's own health endpoints.
	EgressHealthListen string `default:"127.0.0.1:50062" help:"Address of the egress proxy's own /livez and /readyz: host:port, or unix:<path|@abstract>."`
	// HealthListen serves the health of both proxies, for probes.
	HealthListen string `default:"127.0.0.1:50063" help:"Address of /livez, and of /readyz once both proxies are ready, for probes such as Kubernetes': host:port, or unix:<path|@abstract>. Use 0.0.0.0:<port> to probe it from outside the host."`
	// Mirror configures the proxies and reporting.
	Mirror Config `embed:""`
}

// Validate checks that the mirror configuration is usable.
func (c ProxyConfig) Validate() error {
	return errors.WithStack(c.Mirror.Validate())
}

// wiring connects the proxies to the addresses on the command line.
func (c ProxyConfig) wiring() Wiring {
	return Wiring{
		Listen:          c.Listen,
		Reference:       c.ReferenceURL,
		Candidate:       c.CandidateURL,
		ReferenceEgress: c.ReferenceEgressListen,
		CandidateEgress: c.CandidateEgressListen,
		EgressHealth:    c.EgressHealthListen,
		Health:          Some(c.HealthListen),
	}
}

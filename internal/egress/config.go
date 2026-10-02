package egress

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/proxy"
)

// Config contains the command-line configuration for an egress proxy.
type Config struct {
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
	Destinations map[string]string `name:"destination" placeholder:"HOST=URL" help:"Upstream URL for requests to HOST: http, https, h2c, http+unix:<socket>, or h2c+unix:<socket>. Repeat for each dependency."`
	// MatchWindow bounds how long an unused reference response stays available,
	// and how long a candidate request waits for one.
	MatchWindow time.Duration `default:"5s" help:"How long reference and candidate requests wait to be paired."`
	// RecordingBufferBytes limits recorded reference responses across all requests.
	RecordingBufferBytes int `default:"33554432" help:"Maximum reference response bytes recorded across all requests."`
	// CandidateMaxInFlight limits concurrent candidate requests.
	CandidateMaxInFlight int `default:"64" help:"Maximum concurrent candidate requests before quarantine."`
	// Descriptors supplies protobuf descriptors for wire decoding.
	Descriptors descriptors.Config `embed:""`
	// Config holds the server and forwarding limits shared with other proxies.
	proxy.Config `embed:""`
}

// NewConfig returns the default egress configuration.
func NewConfig() Config {
	config := Config{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply egress defaults"))
	}
	return config
}

// Validate checks that the egress has usable resource limits.
func (c Config) Validate() error {
	if c.MatchWindow <= 0 {
		return errors.New("match window must be positive")
	}
	positiveSizes := []struct {
		name  string
		value int
	}{
		{name: "recording buffer size", value: c.RecordingBufferBytes},
		{name: "candidate maximum in-flight requests", value: c.CandidateMaxInFlight},
	}
	for _, size := range positiveSizes {
		if size.value <= 0 {
			return errors.Errorf("%s must be positive", size.name)
		}
	}
	return errors.WithStack(c.Config.Validate())
}

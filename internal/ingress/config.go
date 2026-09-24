package ingress

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
)

// Config contains the command-line configuration for an ingress proxy.
type Config struct {
	// Listen is the address for the ingress HTTP server.
	Listen string `default:"127.0.0.1:50050" help:"Address for the ingress HTTP server."`
	// Reference is the authoritative backend URL.
	Reference string `required:"" help:"Reference backend URL using http, https, or h2c."`
	// Candidate is the mirrored backend URL.
	Candidate string `required:"" help:"Candidate backend URL using a literal loopback IP and http, https, or h2c."`
	// CandidateTimeout limits the duration of a candidate request.
	CandidateTimeout time.Duration `default:"30s" help:"Maximum duration of a candidate request."`
	// CandidateMaxInFlight limits concurrent candidate requests.
	CandidateMaxInFlight int `default:"64" help:"Maximum concurrent candidate requests before quarantine."`
	// CandidateBufferBytes limits queued request data across all candidates.
	CandidateBufferBytes int `default:"1048576" help:"Maximum request bytes buffered across candidates before quarantine."`
	// MaxInFlightRequests limits concurrent ingress requests.
	MaxInFlightRequests int `default:"256" help:"Maximum concurrent ingress requests."`
	// MaxConnections limits concurrent client connections.
	MaxConnections int `default:"512" help:"Maximum concurrent client connections."`
	// ReadHeaderTimeout limits the duration spent reading request headers.
	ReadHeaderTimeout time.Duration `default:"10s" help:"Maximum duration for reading request headers."`
	// IdleTimeout limits how long an idle client connection remains open.
	IdleTimeout time.Duration `default:"1m" help:"Maximum idle client connection duration."`
	// MaxHeaderBytes limits the size of request headers.
	MaxHeaderBytes int `default:"65536" help:"Maximum request header size in bytes."`
	// ShutdownTimeout limits graceful server shutdown.
	ShutdownTimeout time.Duration `default:"10s" help:"Maximum graceful shutdown duration."`
}

// NewConfig returns the default ingress configuration.
func NewConfig() Config {
	// ApplyDefaults validates required fields, so seed them while applying tag defaults.
	config := Config{Reference: "placeholder", Candidate: "placeholder"}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply ingress defaults"))
	}
	config.Reference = ""
	config.Candidate = ""
	return config
}

// Validate checks that the ingress resource limits are usable.
func (c Config) Validate() error {
	positiveDurations := []struct {
		name  string
		value time.Duration
	}{
		{name: "candidate timeout", value: c.CandidateTimeout},
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
		{name: "candidate maximum in-flight requests", value: c.CandidateMaxInFlight},
		{name: "candidate buffer size", value: c.CandidateBufferBytes},
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

// Package statsd is the StatsD protocol, which relays the StatsD metrics of a
// mirror's services over datagram sockets. Reference metrics reach the metrics
// agent unchanged, and candidate metrics are discarded, so the candidate never
// reports metrics as the service.
package statsd

import (
	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
)

// Config contains the command-line configuration for the StatsD protocol. Its flags
// are named with the prefix statsd-.
type Config struct {
	// ReferenceListen receives the reference's metrics, which are forwarded unchanged.
	ReferenceListen string `name:"statsd-reference-listen" placeholder:"ADDRESS" help:"Address the reference sends its StatsD metrics to, which are forwarded to --statsd-forward unchanged: host:port for UDP, or unix:<path|@abstract> for a unix datagram socket."`
	// CandidateListen receives the candidate's metrics, which are discarded.
	CandidateListen string `name:"statsd-candidate-listen" placeholder:"ADDRESS" help:"Address the candidate sends its StatsD metrics to, which are discarded: host:port for UDP, or unix:<path|@abstract> for a unix datagram socket."`
	// Forward is the metrics agent that reference metrics reach.
	Forward string `name:"statsd-forward" placeholder:"ADDRESS" help:"Metrics agent that the reference's StatsD metrics are forwarded to, such as unix:@veneur.sock: host:port for UDP, or unix:<path|@abstract> for a unix datagram socket."`
}

// NewConfig returns the default StatsD configuration, which relays nothing.
func NewConfig() Config {
	config := Config{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply StatsD defaults"))
	}
	return config
}

// Validate checks that reference metrics have somewhere to go, and that a forward address has metrics to forward.
func (c Config) Validate() error {
	if c.ReferenceListen != "" && c.Forward == "" {
		return errors.New("StatsD reference listener needs a forward address")
	}
	if c.Forward != "" && c.ReferenceListen == "" {
		return errors.New("StatsD forward address needs a reference listener")
	}
	return nil
}

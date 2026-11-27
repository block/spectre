// Package statsd is the StatsD protocol, which relays the StatsD metrics of a
// mirror's services over datagram sockets. Reference metrics reach the metrics
// agent unchanged, and candidate metrics are discarded, so the candidate never
// reports metrics as the service.
package statsd

import (
	"time"

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
	// Window is how long each comparison of the two sides' metrics covers.
	Window time.Duration `name:"statsd-window" default:"1m" help:"Interval over which the sides' StatsD metrics are totalled and compared, when scripts declare StatsD rules."`
	// Settle is how long after a window ends its comparison waits for late metrics.
	Settle time.Duration `name:"statsd-settle" default:"10s" help:"Time after a StatsD window ends that its comparison waits for metrics still on their way."`
	// CandidateStartupTimeout bounds how long comparison waits for the candidate's first compared metric.
	CandidateStartupTimeout time.Duration `name:"statsd-candidate-startup-timeout" default:"5m" help:"Time StatsD comparison waits for the candidate's first compared metric before it compares anyway."`
	// MaxSeries bounds the distinct series in a window.
	MaxSeries int `name:"statsd-max-series" default:"10000" help:"Most distinct StatsD series in one window. Beyond it the candidate is quarantined."`
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
	if c.Window <= 0 {
		return errors.New("StatsD window must be positive")
	}
	if c.Settle < 0 || c.CandidateStartupTimeout < 0 {
		return errors.New("StatsD settle delay and candidate startup timeout must not be negative")
	}
	if c.MaxSeries <= 0 {
		return errors.New("StatsD series limit must be positive")
	}
	return nil
}

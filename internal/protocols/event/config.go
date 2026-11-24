package event

import (
	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/netaddr"
)

// Config contains the command-line configuration for the event protocol. Its flags are
// named with the prefix event-.
type Config struct {
	// ReferenceListen receives the reference's events. It must stay local, because any
	// caller on it is trusted to be the reference.
	ReferenceListen string `name:"event-reference-listen" placeholder:"ADDRESS" help:"Address the reference reports its events to, on a loopback IP or unix socket: host:port, or unix:<path|@abstract>. Setting it turns the event protocol on."`
	// CandidateListen receives the candidate's events and serves the runs it must start.
	CandidateListen string `name:"event-candidate-listen" default:"127.0.0.1:50081" help:"Address the candidate receives runs from and reports its events to, on a loopback IP or unix socket: host:port, or unix:<path|@abstract>."`
	// TriggerQueue bounds the runs waiting for the candidate to receive them.
	TriggerQueue int `name:"event-trigger-queue" default:"64" help:"Maximum runs waiting for the candidate to receive them. Beyond it, new runs are not shadowed."`
}

// NewConfig returns the default event protocol configuration, which serves nothing.
func NewConfig() Config {
	config := Config{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply event defaults"))
	}
	return config
}

// Enabled reports whether the event protocol is served.
func (c Config) Enabled() bool {
	return c.ReferenceListen != ""
}

// Validate checks the parsed config.
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if !isLocal(c.ReferenceListen) {
		return errors.New("event reference listener must use a loopback IP address or a unix socket")
	}
	if !isLocal(c.CandidateListen) {
		return errors.New("event candidate listener must use a loopback IP address or a unix socket")
	}
	if c.TriggerQueue <= 0 {
		return errors.New("event trigger queue must be positive")
	}
	return nil
}

func isLocal(listen string) bool {
	address := netaddr.ParseListen(listen)
	return address.IsUnix() || address.IsLoopback()
}

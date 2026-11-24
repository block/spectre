package statsd

import (
	"context"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocols"
)

// server forwards the reference's metrics and discards the candidate's.
type server struct {
	config Config
}

func newServer(config Config) *server {
	return &server{config: config}
}

// Serve relays the open listeners until ctx ends or one fails, which stops the other.
// With no listeners, it waits for ctx.
func (s *server) Serve(ctx context.Context, listeners protocols.Listeners) error {
	ctx, cancel := context.WithCancel(logger.WithSubsystem(ctx, "statsd"))
	defer cancel()
	relays := []func() error{}
	if conn, ok := listeners.Datagrams[ReferenceListener]; ok {
		forward := netaddr.ParsePacket(s.config.Forward)
		relays = append(relays, func() error { return Forward(ctx, conn, forward) })
	}
	if conn, ok := listeners.Datagrams[CandidateListener]; ok {
		relays = append(relays, func() error { return Discard(ctx, conn) })
	}
	if len(relays) == 0 {
		<-ctx.Done()
		return nil
	}
	finished := make(chan error, len(relays))
	for _, relay := range relays {
		go func() {
			err := relay()
			cancel()
			finished <- err
		}()
	}
	relayErrors := []error{}
	for range relays {
		relayErrors = append(relayErrors, <-finished)
	}
	return errors.Join(relayErrors...)
}

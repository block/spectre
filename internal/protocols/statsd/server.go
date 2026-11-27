package statsd

import (
	"context"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/statsd/metric"
)

// server forwards the reference's metrics and discards the candidate's, comparing them when it can.
type server struct {
	config      Config
	health      *health.Handler
	quarantiner engine.Quarantiner
	quarantines protocols.QuarantineStatus
	resolver    *resolver
}

func newServer(ctx context.Context, config Config, side protocols.Side, resolver *resolver) *server {
	if resolver.declared() && (config.ReferenceListen == "" || config.CandidateListen == "") {
		logger.FromContext(ctx).WarnContext(ctx, "Scripts declare StatsD rules, but comparing needs both StatsD listeners")
	}
	return &server{config: config, health: side.Health, quarantiner: side.Quarantiner, quarantines: side.Quarantines, resolver: resolver}
}

// Serve relays the open listeners until ctx ends or one fails, which stops the other.
// With no listeners, it waits for ctx.
func (s *server) Serve(ctx context.Context, listeners protocols.Listeners) error {
	ctx, cancel := context.WithCancel(logger.WithSubsystem(ctx, "statsd"))
	defer cancel()
	s.health.SetReady(true)
	reference, hasReference := listeners.Datagrams[ReferenceListener]
	candidate, hasCandidate := listeners.Datagrams[CandidateListener]
	observers := map[metric.Side]func([]byte){metric.Reference: unobserved, metric.Candidate: unobserved}
	comparing := None[*comparator]()
	if hasReference && hasCandidate && s.resolver.declared() {
		compare := newComparator(ctx, s.config, s.resolver, s.quarantiner, s.quarantines)
		observers[metric.Reference], observers[metric.Candidate] = compare.observe(metric.Reference), compare.observe(metric.Candidate)
		comparing = Some(compare)
		logger.FromContext(ctx).InfoContext(ctx, "Comparing StatsD metrics", "window", s.config.Window)
	}
	relays := []func() error{}
	if hasReference {
		forward := netaddr.ParsePacket(s.config.Forward)
		relays = append(relays, func() error { return Forward(ctx, reference, forward, observers[metric.Reference]) })
	}
	if hasCandidate {
		relays = append(relays, func() error { return Discard(ctx, candidate, observers[metric.Candidate]) })
	}
	if compare, ok := comparing.Get(); ok {
		relays = append(relays, func() error {
			compare.run(ctx)
			return nil
		})
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

// unobserved is the observer of a side whose metrics are not compared.
func unobserved([]byte) {}

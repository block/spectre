package statsd

import (
	"context"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/statsd/scripted"
	"github.com/block/spectre/internal/state"
)

// The listener names, which Provider.Listens and server.Serve agree on.
const (
	// ReferenceListener receives the reference's metrics.
	ReferenceListener = "reference"
	// CandidateListener receives the candidate's metrics.
	CandidateListener = "candidate"
)

// subsystem tags the StatsD logs.
const subsystem = "statsd"

// Provider is the StatsD protocol. Its metrics are egress, compared in windows rather
// than paired by the engine. The handle reads what scripts declared.
type Provider struct {
	binding javascript.Binding
	handle  scripted.Handle
}

// NewProvider returns the StatsD provider.
func NewProvider() *Provider {
	binding, handle := scripted.NewBinding()
	return &Provider{binding: binding, handle: handle}
}

// Protocol is "statsd".
func (p *Provider) Protocol() model.Protocol {
	return scripted.Protocol
}

// Kinds describes the metrics the services send, for the migration state.
func (p *Provider) Kinds() []state.Kind {
	return []state.Kind{{
		Direction:   model.Egress,
		Protocol:    scripted.Protocol,
		Description: "Module that sends StatsD metrics to a metrics agent.",
	}}
}

// Scripts is the StatsD binding of the script module.
func (p *Provider) Scripts() javascript.Binding {
	return p.binding
}

// Listens names the datagram listeners that config sets.
func (p *Provider) Listens(config Config) []protocols.Listen { //nolint:narrowdeps // Provider's signature.
	listens := []protocols.Listen{}
	if config.ReferenceListen != "" {
		listens = append(listens, protocols.Listen{Name: ReferenceListener, Address: config.ReferenceListen, Transport: protocols.Datagram})
	}
	if config.CandidateListen != "" {
		listens = append(listens, protocols.Listen{Name: CandidateListener, Address: config.CandidateListen, Transport: protocols.Datagram})
	}
	return listens
}

// New builds the relay on the egress side, comparing the sides' metrics when scripts declare StatsD rules.
func (p *Provider) New(ctx context.Context, config Config, runtime protocols.Runtime) (protocols.Server, error) {
	ctx = logger.WithSubsystem(ctx, subsystem)
	side, hasEgress := runtime.Egress.Get()
	if !hasEgress {
		return nil, errors.New("StatsD needs an egress side")
	}
	if side.Quarantines == nil {
		return nil, errors.New("StatsD needs the candidate's quarantine status")
	}
	resolver, err := newResolver(runtime.Scripts, p.handle)
	if err != nil {
		return nil, errors.Wrap(err, "configure StatsD")
	}
	return newServer(ctx, config, side, resolver), nil
}

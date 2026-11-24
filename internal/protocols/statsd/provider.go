package statsd

import (
	"context"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/state"
)

// Protocol is "statsd".
const Protocol model.Protocol = "statsd"

// The listener names, which Provider.Listens and server.Serve agree on.
const (
	// ReferenceListener receives the reference's metrics.
	ReferenceListener = "reference"
	// CandidateListener receives the candidate's metrics.
	CandidateListener = "candidate"
)

// Provider is the StatsD protocol. Its metrics bypass the engine, so it has no
// kinds, and scripts cannot declare them.
type Provider struct{}

// NewProvider returns the StatsD provider.
func NewProvider() *Provider {
	return &Provider{}
}

// Protocol is "statsd".
func (p *Provider) Protocol() model.Protocol {
	return Protocol
}

// Kinds is empty, because the engine never pairs metrics.
func (p *Provider) Kinds() []state.Kind {
	return nil
}

// Scripts adds nothing to the script module, because scripts cannot declare metrics yet.
func (p *Provider) Scripts() javascript.Binding {
	return javascript.NewEmptyBinding(Protocol)
}

// Listens names the datagram listeners that config sets.
func (p *Provider) Listens(config Config) []protocols.Listen {
	listens := []protocols.Listen{}
	if config.ReferenceListen != "" {
		listens = append(listens, protocols.Listen{Name: ReferenceListener, Address: config.ReferenceListen, Transport: protocols.Datagram})
	}
	if config.CandidateListen != "" {
		listens = append(listens, protocols.Listen{Name: CandidateListener, Address: config.CandidateListen, Transport: protocols.Datagram})
	}
	return listens
}

// New builds the relay for config.
func (p *Provider) New(_ context.Context, config Config, _ protocols.Runtime) (protocols.Server, error) {
	return newServer(config), nil
}

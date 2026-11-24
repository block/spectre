// Package event mirrors background work, such as cron ticks and queued jobs.
//
// The reference reports each start, SPECTRE streams the candidate the same work under a run ID,
// and egress pairs the events both runs produce like any other dependency call.
package event

import (
	"context"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/event/script"
	"github.com/block/spectre/internal/state"
)

// The listener names, which Provider.Listens and Server.Serve agree on.
const (
	// ReferenceListener receives the reference's events.
	ReferenceListener = "reference"
	// CandidateListener receives the candidate's events and serves its runs.
	CandidateListener = "candidate"
)

// Provider is the event protocol. The handle reads what scripts declared, so no package
// state is needed.
type Provider struct {
	binding javascript.Binding
	handle  script.Handle
}

// NewProvider returns the event provider.
func NewProvider() *Provider {
	binding, handle := script.NewBinding()
	return &Provider{binding: binding, handle: handle}
}

// Protocol is "event".
func (p *Provider) Protocol() model.Protocol {
	return script.Protocol
}

// Kinds describes the kinds of IO that events carry, for the migration state.
func (p *Provider) Kinds() []state.Kind {
	return []state.Kind{
		{
			Direction:   model.Ingress,
			Protocol:    script.Protocol,
			Description: "Module that runs background work, such as a scheduled job, a timer, a queued job or a consumed message.",
			Settings: []state.Setting{{
				Name:        "types",
				Required:    true,
				Description: "Each reference event type that starts the node's work.",
				Key:         "<event type>",
				Unique:      true,
			}},
		},
		{
			Direction:   model.Egress,
			Protocol:    script.Protocol,
			Description: "Module that sends events that start other work, such as queuing a job, scheduling a task or publishing a message.",
		},
	}
}

// Scripts is the event binding of the script module.
func (p *Provider) Scripts() javascript.Binding {
	return p.binding
}

// Listens names the reference and candidate listeners, when events are served.
func (p *Provider) Listens(config Config) []protocols.Listen {
	if !config.Enabled() {
		return nil
	}
	return []protocols.Listen{
		{Name: ReferenceListener, Address: config.ReferenceListen},
		{Name: CandidateListener, Address: config.CandidateListen},
	}
}

// New builds the event server. When events are not served, the server only marks both
// directions ready and waits.
func (p *Provider) New(_ context.Context, config Config, runtime protocols.Runtime) (protocols.Server, error) {
	ingress, hasIngress := runtime.Ingress.Get()
	egress, hasEgress := runtime.Egress.Get()
	if !hasIngress || !hasEgress {
		return nil, errors.New("events need an ingress and an egress side")
	}
	if !config.Enabled() {
		return newIdle(ingress.Health, egress.Health), nil
	}
	server, err := New(config, p.handle, runtime, ingress, egress)
	if err != nil {
		return nil, errors.Wrap(err, "configure events")
	}
	return server, nil
}

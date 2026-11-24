package mysql

import (
	"context"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/mysql/scripted"
	"github.com/block/spectre/internal/state"
)

// The listener names, which Provider.Listens and Server.Serve agree on.
const (
	// ReferenceListener receives the reference's statements.
	ReferenceListener = "reference"
	// CandidateListener receives the candidate's statements.
	CandidateListener = "candidate"
)

// Provider is the MySQL protocol. The handle reads what scripts declared, so no
// package state is needed.
type Provider struct {
	binding javascript.Binding
	handle  scripted.Handle
}

// NewProvider returns the MySQL provider.
func NewProvider() *Provider {
	binding, handle := scripted.NewBinding()
	return &Provider{binding: binding, handle: handle}
}

// Protocol is "mysql".
func (p *Provider) Protocol() model.Protocol {
	return scripted.Protocol
}

// Kinds describes the kinds of IO that MySQL proxies, for the migration state.
func (p *Provider) Kinds() []state.Kind {
	return []state.Kind{{
		Direction:   model.Egress,
		Protocol:    scripted.Protocol,
		Description: "Module that sends statements to a MySQL database.",
	}}
}

// Scripts is the MySQL binding of the script module.
func (p *Provider) Scripts() javascript.Binding {
	return p.binding
}

// Listens names the reference and candidate listeners, when MySQL is proxied.
func (p *Provider) Listens(config Config) []protocols.Listen {
	if !config.Enabled() {
		return nil
	}
	return []protocols.Listen{
		{Name: ReferenceListener, Address: config.ReferenceListen},
		{Name: CandidateListener, Address: config.CandidateListen},
	}
}

// New builds the MySQL proxy on the egress side. When MySQL is not proxied, the
// server only marks egress ready and waits.
func (p *Provider) New(_ context.Context, config Config, runtime protocols.Runtime) (protocols.Server, error) {
	side, hasEgress := runtime.Egress.Get()
	if !hasEgress {
		return nil, errors.New("MySQL needs an egress side")
	}
	if !config.Enabled() {
		return newIdle(side.Health), nil
	}
	server, err := New(config, p.handle, runtime, side)
	if err != nil {
		return nil, errors.Wrap(err, "configure MySQL")
	}
	return server, nil
}

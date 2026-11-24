package http

import (
	"context"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/httpservice"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/http/script"
	"github.com/block/spectre/internal/state"
)

// Provider is the HTTP protocol, including gRPC and Connect. The handle reads what
// scripts declared, so no package state is needed.
type Provider struct {
	binding javascript.Binding
	handle  script.Handle
}

// NewProvider returns the HTTP provider.
func NewProvider() *Provider {
	binding, handle := script.New()
	return &Provider{binding: binding, handle: handle}
}

// Protocol is "http".
func (p *Provider) Protocol() model.Protocol {
	return script.Protocol
}

// Kinds describes the kinds of IO that HTTP proxies, for the migration state.
func (p *Provider) Kinds() []state.Kind {
	return []state.Kind{
		{
			Direction:   model.Ingress,
			Protocol:    script.Protocol,
			Description: "Module that receives HTTP requests to the service, including gRPC and Connect.",
			Settings: []state.Setting{{
				Name:        "routes",
				Required:    true,
				Description: "Each route the node handles.",
				Key:         "<METHOD> /<path>",
				Unique:      true,
			}},
		},
		{
			Direction:   model.Egress,
			Protocol:    script.Protocol,
			Description: "Module that sends HTTP requests to another service, including gRPC and Connect.",
		},
	}
}

// Scripts is the HTTP binding of the script module.
func (p *Provider) Scripts() javascript.Binding {
	return p.binding
}

// Listens names the ingress listener and the two egress listeners.
func (p *Provider) Listens(config Config) []protocols.Listen {
	return []protocols.Listen{
		{Name: IngressListener, Address: config.Listen},
		{Name: ReferenceEgressListener, Address: config.ReferenceEgressListen},
		{Name: CandidateEgressListener, Address: config.CandidateEgressListen},
	}
}

// New builds ingress and egress on the shared sides. Each has a backend transport of its own.
//
//nolint:narrowdeps // Provider[Config] fixes the signature.
func (p *Provider) New(ctx context.Context, config Config, runtime protocols.Runtime) (protocols.Server, error) {
	ingressSide, hasIngress := runtime.Ingress.Get()
	egressSide, hasEgress := runtime.Egress.Get()
	if !hasIngress || !hasEgress {
		return nil, errors.New("HTTP needs an ingress side and an egress side")
	}
	egressConfig, ingressConfig := config.egressConfig(), config.ingressConfig()
	egressTransport := httpservice.NewTransport(egressConfig.TransportConfig)
	egress, err := NewEgress(logger.WithSubsystem(ctx, egressSubsystem), egressConfig, p.handle, runtime, egressSide, egressTransport)
	if err != nil {
		egressTransport.CloseIdleConnections()
		return nil, errors.Wrap(err, "configure egress")
	}
	ingressTransport := httpservice.NewTransport(ingressConfig.TransportConfig)
	loader := Some[DescriptorLoader](descriptors.NewReflectionLoader())
	ingress, err := NewIngress(logger.WithSubsystem(ctx, ingressSubsystem), ingressConfig, p.handle, runtime, ingressSide, ingressTransport, loader)
	if err != nil {
		egressTransport.CloseIdleConnections()
		ingressTransport.CloseIdleConnections()
		return nil, errors.Wrap(err, "configure ingress")
	}
	return newServer(ingress, egress, ingressTransport, egressTransport), nil
}

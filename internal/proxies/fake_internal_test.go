package proxies

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/comparison/javascript/javascripttest"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/state"
)

// fakeConfig is the config of fakeProvider, which has no flags.
type fakeConfig struct{}

func (fakeConfig) Validate() error { return nil }

// fakeProvider is a made-up protocol that proxies both directions, so the mirror is
// tested without a real protocol. Its servers report what the mirror gave them.
type fakeProvider struct {
	binding javascript.Binding
	// addresses are the listeners it names, "main" for the first and so on.
	addresses []string
	// built receives the runtime of each server the mirror builds.
	built chan protocols.Runtime
	// served receives the listeners of each server the mirror runs.
	served chan protocols.Listeners
}

func newFakeProvider(addresses ...string) *fakeProvider {
	return &fakeProvider{
		binding:   javascripttest.Binding(),
		addresses: addresses,
		built:     make(chan protocols.Runtime, 1),
		served:    make(chan protocols.Listeners, 1),
	}
}

func (p *fakeProvider) Protocol() model.Protocol { return javascripttest.Protocol }

func (p *fakeProvider) Kinds() []state.Kind {
	return []state.Kind{
		{
			Direction:   model.Ingress,
			Protocol:    javascripttest.Protocol,
			Description: "Receives fake requests.",
			Settings:    []state.Setting{{Name: "routes", Required: true, Description: "Each route.", Key: "<route>", Unique: true}},
		},
		{Direction: model.Egress, Protocol: javascripttest.Protocol, Description: "Sends fake requests."},
	}
}

func (p *fakeProvider) Scripts() javascript.Binding { return p.binding }

func (p *fakeProvider) Listens(fakeConfig) []protocols.Listen {
	listens := make([]protocols.Listen, 0, len(p.addresses))
	for i, address := range p.addresses {
		listens = append(listens, protocols.Listen{Name: []string{"main", "other"}[i], Address: address, Transport: protocols.Stream})
	}
	return listens
}

func (p *fakeProvider) New(_ context.Context, _ fakeConfig, runtime protocols.Runtime) (protocols.Server, error) {
	p.built <- runtime
	return &fakeServer{runtime: runtime, served: p.served}, nil
}

// fakeServer marks egress ready once it serves, as a real protocol does.
type fakeServer struct {
	runtime protocols.Runtime
	served  chan protocols.Listeners
}

func (s *fakeServer) Serve(ctx context.Context, listeners protocols.Listeners) error {
	s.served <- listeners
	if side, ok := s.runtime.Egress.Get(); ok {
		side.Health.SetReady(true)
	}
	<-ctx.Done()
	return nil
}

// fakeRegistry registers provider alone.
func fakeRegistry(t *testing.T, provider *fakeProvider) *protocols.Registry {
	t.Helper()
	registry := protocols.NewRegistry()
	assert.NoError(t, registry.Register(provider))
	return registry
}

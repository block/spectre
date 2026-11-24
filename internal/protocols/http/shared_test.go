package http_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	httpprotocol "github.com/block/spectre/internal/protocols/http"
	"github.com/block/spectre/internal/protocols/http/script"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/schema"
	"github.com/block/spectre/internal/state"
)

// shared is the engine and candidate admission that the mirror owns and passes to
// each proxy. Tests own them the same way, and close and shut them down themselves.
type shared struct {
	Engine            *engine.Engine
	IngressCandidates *proxy.Candidates
	EgressCandidates  *proxy.Candidates
	Calls             *proxy.CallCounter
	HTTP              script.Handle
}

func newShared(ctx context.Context, t *testing.T, engineConfig engine.Config, candidates proxy.CandidatesConfig) *shared {
	t.Helper()
	built := &shared{
		IngressCandidates: proxy.NewCandidates(candidates),
		EgressCandidates:  proxy.NewCandidates(candidates),
		Calls:             proxy.NewCallCounter(),
	}
	binding, handle := script.New()
	built.HTTP = handle
	pairing, err := engine.NewEngine(ctx, engineConfig, []javascript.Binding{binding}, built.IngressCandidates, built.EgressCandidates, Some[engine.CallCounter](built.Calls))
	assert.NoError(t, err)
	built.Engine = pairing
	return built
}

// runtime is what the mirror shares with the provider, and gives it the sides it names.
func (s *shared) runtime(nodes Option[[]state.KindNode]) protocols.Runtime {
	return protocols.Runtime{
		Schema:  s.Engine.Schema(),
		Scripts: s.Engine.Scripts(),
		Ingress: Some(protocols.Side{
			Reference:      s.Engine.IngressReference(script.Protocol),
			Candidate:      s.Engine.IngressCandidate(script.Protocol),
			Candidates:     s.IngressCandidates,
			Quarantiner:    s.IngressCandidates,
			Health:         health.New(http.NotFoundHandler()),
			Nodes:          nodes,
			SourceResponse: func(model.Route) Option[schema.TypeRef] { return None[schema.TypeRef]() },
		}),
		Egress: Some(protocols.Side{
			Reference:   s.Engine.EgressReference(script.Protocol),
			Candidate:   s.Engine.EgressCandidate(script.Protocol),
			Candidates:  s.EgressCandidates,
			Quarantiner: s.EgressCandidates,
			Health:      health.New(http.NotFoundHandler()),
			SourceResponse: func(route model.Route) Option[schema.TypeRef] {
				return s.Engine.EgressSourceResponse(script.Protocol, route)
			},
		}),
	}
}

// nodesOf are the nodes of the migration state that list ingress[http] routes, as the mirror reads them.
func nodesOf(migration Option[state.State]) Option[[]state.KindNode] {
	loaded, ok := migration.Get()
	if !ok {
		return None[[]state.KindNode]()
	}
	return Some(loaded.Nodes(state.Kind{Direction: model.Ingress, Protocol: script.Protocol}))
}

// httpKinds are the kinds of IO that the HTTP provider describes, for loading migration states.
func httpKinds(t *testing.T) state.Kinds {
	t.Helper()
	registry := protocols.NewRegistry()
	assert.NoError(t, registry.Register(httpprotocol.NewProvider()))
	return registry.Kinds()
}

// Close stops candidate work before the servers stop, as the mirror does.
func (s *shared) Close() {
	s.Engine.Close()
	s.IngressCandidates.Close()
	s.EgressCandidates.Close()
}

// Shutdown waits for the work that outlives the servers, as the mirror does once they stop.
func (s *shared) Shutdown(ctx context.Context) error {
	return errors.Join(
		errors.WithStack(s.IngressCandidates.Shutdown(ctx)),
		errors.WithStack(s.EgressCandidates.Shutdown(ctx)),
		errors.WithStack(s.Engine.Shutdown(ctx)),
	)
}

// testIngress is an ingress on shared machinery that the test owns.
type testIngress struct {
	*httpprotocol.Ingress
	shared *shared
}

// newIngress builds an ingress as the mirror does, on candidate limits that the mirror defaults.
func newIngress(
	t *testing.T,
	config httpprotocol.IngressConfig,
	engineConfig engine.Config,
	transport http.RoundTripper,
	loader Option[httpprotocol.DescriptorLoader],
) (*testIngress, error) {
	t.Helper()
	return newIngressWithState(t, config, engineConfig, transport, loader, None[state.State]())
}

func newIngressWithState(
	t *testing.T,
	config httpprotocol.IngressConfig,
	engineConfig engine.Config,
	transport http.RoundTripper,
	loader Option[httpprotocol.DescriptorLoader],
	migration Option[state.State],
) (*testIngress, error) {
	t.Helper()
	return newIngressOn(t, newShared(t.Context(), t, engineConfig, proxy.NewCandidatesConfig()), config, transport, loader, migration)
}

func newIngressOn(
	t *testing.T,
	built *shared,
	config httpprotocol.IngressConfig,
	transport http.RoundTripper,
	loader Option[httpprotocol.DescriptorLoader],
	migration Option[state.State],
) (*testIngress, error) {
	t.Helper()
	runtime := built.runtime(nodesOf(migration))
	ingress, err := httpprotocol.NewIngress(t.Context(), config, built.HTTP, runtime, runtime.Ingress.MustGet(), transport, loader)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &testIngress{Ingress: ingress, shared: built}, nil
}

// Candidates reports the ingress candidate quarantine state.
func (i *testIngress) Candidates() *proxy.Candidates {
	return i.shared.IngressCandidates
}

// Calls returns the requests on each route whose comparison completed.
func (i *testIngress) Calls() model.Calls {
	return i.shared.Calls.Calls()
}

// Shutdown stops accepting candidate work and waits for active mirrors to finish.
func (i *testIngress) Shutdown(ctx context.Context) error {
	return i.shared.Shutdown(ctx)
}

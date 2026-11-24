package protocols

import (
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/schema"
	"github.com/block/spectre/internal/state"
)

// Runtime is what the mirror shares with a provider. It holds only the
// provider's own protocol, so a provider cannot reach another's traffic.
type Runtime struct {
	Schema  *schema.Schema
	Scripts *comparison.Scripts
	// Ingress and Egress are present for each direction that the provider's Kinds name.
	Ingress Option[Side]
	Egress  Option[Side]
}

// Side is one direction's shared machinery: the engine's points for this
// protocol, the candidate admission every protocol shares, and readiness.
type Side struct {
	Reference engine.Acceptor
	Candidate engine.Acceptor
	// Candidates admits candidate work, and the mirror stops it before the provider.
	Candidates *proxy.Candidates
	// Quarantiner quarantines the candidate. Ingress reasons name the migration
	// state node that owns their route, which Candidates alone would not.
	Quarantiner engine.Quarantiner
	// Health is this provider's own readiness, which it sets. The mirror combines all providers' handlers.
	Health *health.Handler
	// Nodes are the migration-state nodes with this kind, when the mirror has a state.
	Nodes Option[[]state.KindNode]
	// SourceResponse is the type that replacements read an endpoint's responses as.
	// It is None for every ingress route.
	SourceResponse func(route model.Route) Option[schema.TypeRef]
}

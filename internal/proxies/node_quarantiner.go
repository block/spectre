package proxies

import (
	"context"

	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/state"
)

// nodeQuarantiner names the migration state node that owns an ingress route in
// each reason, so a quarantine also says which node to fix.
type nodeQuarantiner struct {
	candidates *proxy.Candidates
	migration  Option[state.State]
}

// newNodeQuarantiner quarantines candidates, naming the node that migration says
// owns each reason's route, which is the state key that listed the request.
func newNodeQuarantiner(candidates *proxy.Candidates, migration Option[state.State]) *nodeQuarantiner {
	return &nodeQuarantiner{candidates: candidates, migration: migration}
}

func (q *nodeQuarantiner) Quarantine(ctx context.Context, reason model.QuarantineReason) {
	traffic, hasTraffic := reason.Traffic.Get()
	migration, hasMigration := q.migration.Get()
	if hasTraffic && hasMigration {
		if node, owned := migration.Owner(traffic); owned {
			reason.Node = Some(node)
		}
	}
	q.candidates.Quarantine(ctx, reason)
}

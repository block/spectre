package proxies

import (
	"context"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/state"
)

// shared is what every protocol shares: one engine, candidate admission per direction,
// and the ingress call counter. The mirror closes and shuts it down, never a protocol.
type shared struct {
	Engine             *engine.Engine
	IngressCandidates  *proxy.Candidates
	EgressCandidates   *proxy.Candidates
	IngressQuarantiner *nodeQuarantiner
	Calls              *proxy.CallCounter
}

// newShared loads the scripts once and builds the engine, which quarantines each
// direction's candidates through that direction's admission.
func newShared(ctx context.Context, config engine.Config, candidates proxy.CandidatesConfig,
	bindings []javascript.Binding, migration Option[state.State],
) (*shared, error) {
	ingressCandidates := proxy.NewCandidates(candidates)
	egressCandidates := proxy.NewCandidates(candidates)
	ingressQuarantiner := newNodeQuarantiner(ingressCandidates, migration)
	calls := proxy.NewCallCounter()
	pairing, err := engine.NewEngine(ctx, config, bindings, ingressQuarantiner, egressCandidates, Some[engine.CallCounter](calls))
	if err != nil {
		return nil, errors.Wrap(err, "configure engine")
	}
	return &shared{
		Engine:             pairing,
		IngressCandidates:  ingressCandidates,
		EgressCandidates:   egressCandidates,
		IngressQuarantiner: ingressQuarantiner,
		Calls:              calls,
	}, nil
}

// close stops candidate work without waiting, so servers do not wait on it as they
// stop. Call it before cancelling the servers.
func (s *shared) close() {
	s.Engine.Close()
	s.IngressCandidates.Close()
	s.EgressCandidates.Close()
}

// shutdown waits for the candidate work and engine tasks that outlive the servers.
// Call it after every server has stopped.
func (s *shared) shutdown(ctx context.Context) error {
	return errors.Join(
		errors.WithStack(s.IngressCandidates.Shutdown(ctx)),
		errors.WithStack(s.EgressCandidates.Shutdown(ctx)),
		errors.WithStack(s.Engine.Shutdown(ctx)),
	)
}

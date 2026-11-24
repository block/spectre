package engine

import (
	"context"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// Engine owns the in-flight state that every Acceptor shares.
type Engine struct {
	schema  *schema.Schema
	scripts *comparison.Scripts
	ingress *ingressPairs
	egress  *egressQueue
}

// NewEngine loads the scripts that the engine compares and replays with, checked against the
// bindings' script API. Each direction quarantines through its own quarantiner, and only ingress counts calls.
func NewEngine(
	ctx context.Context,
	config Config,
	bindings []javascript.Binding,
	ingress, egress Quarantiner,
	calls Option[CallCounter],
) (*Engine, error) {
	if ingress == nil || egress == nil {
		return nil, errors.New("quarantiner is required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	scripts, err := comparison.New(ctx, config.Comparison, bindings)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &Engine{
		schema:  scripts.Schema(),
		scripts: scripts,
		ingress: newIngressPairs(scripts, ingress, calls),
		egress:  newEgressQueue(scripts, config.MatchWindow, egress),
	}, nil
}

// Scripts returns the scripts that the engine compares with.
func (e *Engine) Scripts() *comparison.Scripts {
	return e.scripts
}

// Schema returns the types that servers decode messages as.
func (e *Engine) Schema() *schema.Schema {
	return e.schema
}

// EgressSourceResponse returns the type to decode an egress endpoint's reference
// responses as, when replacements read them. Other egress responses are only replayed.
func (e *Engine) EgressSourceResponse(protocol model.Protocol, route model.Route) Option[schema.TypeRef] {
	return e.egress.sourceResponse(protocol, route)
}

// IngressReference returns the acceptor for requests to the reference service.
func (e *Engine) IngressReference(protocol model.Protocol) Acceptor {
	return newIngressReference(e.ingress, protocol)
}

// IngressCandidate returns the acceptor for mirrored requests to the candidate service.
func (e *Engine) IngressCandidate(protocol model.Protocol) Acceptor {
	return newIngressCandidate(e.ingress, protocol)
}

// EgressReference returns the acceptor for the reference's dependency requests.
func (e *Engine) EgressReference(protocol model.Protocol) Acceptor {
	return newEgressReference(e.egress, protocol)
}

// EgressCandidate returns the acceptor for the candidate's dependency requests.
func (e *Engine) EgressCandidate(protocol model.Protocol) Acceptor {
	return newEgressCandidate(e.egress, protocol)
}

// Close stops quarantining the candidate for reference requests it leaves out,
// because the server is stopping candidate work. Call it before stopping candidates.
func (e *Engine) Close() {
	e.egress.close()
}

// Shutdown waits for background work on requests that have already been forwarded.
func (e *Engine) Shutdown(ctx context.Context) error {
	return errors.WithStack(e.egress.shutdown(ctx))
}

func newExchange() Exchange {
	return typeid.Must(typeid.New[Exchange]())
}

func newRequestID() RequestID {
	return typeid.Must(typeid.New[RequestID]())
}

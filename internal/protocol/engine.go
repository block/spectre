package protocol

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/alecthomas/errors"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/schema"
)

// Engine owns the in-flight state that every Acceptor shares.
type Engine struct {
	scripts *comparison.Scripts
	ingress *ingressPairs
	egress  *egressQueue
}

// NewEngine loads the scripts and passes each declaration to the matcher for its
// protocol. A script naming an unregistered protocol fails startup.
func NewEngine(
	ctx context.Context,
	config Config,
	matchers map[javascript.Protocol]Matcher,
	quarantine Quarantiner,
	log *slog.Logger,
) (*Engine, error) {
	if quarantine == nil {
		return nil, errors.New("quarantiner is required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	scripts, err := comparison.New(ctx, config.Comparison, log)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	for _, endpoint := range scripts.Endpoints() {
		matcher, registered := matchers[endpoint.Protocol()]
		if !registered {
			return nil, errors.Errorf("endpoint %q names unregistered protocol %q", endpoint.Pattern(), endpoint.Protocol())
		}
		if err := matcher.Declare(endpoint.Pattern(), endpointDeclaration(endpoint)); err != nil {
			return nil, errors.Wrapf(err, "declare %s endpoint %q", endpoint.Direction(), endpoint.Pattern())
		}
	}
	return &Engine{
		scripts: scripts,
		ingress: newIngressPairs(scripts, quarantine, log),
		egress:  newEgressQueue(scripts, config.MatchWindow, quarantine, log),
	}, nil
}

func endpointDeclaration(endpoint javascript.Endpoint) Declaration {
	switch {
	case endpoint.Direction() == javascript.Egress:
		return DeclarationRequest{Type: endpoint.Type()}
	case endpoint.Ignore():
		return DeclarationIgnore{Reason: fmt.Sprintf("endpoint %q is ignored", endpoint.Pattern())}
	default:
		return DeclarationResponse{Type: endpoint.Type()}
	}
}

// Schema returns the types that servers decode messages as.
func (e *Engine) Schema() *schema.Schema {
	return e.scripts.Schema()
}

// IngressReference returns the acceptor for requests to the reference service.
func (e *Engine) IngressReference(protocol javascript.Protocol) Acceptor {
	return newIngressReference(e.ingress, protocol)
}

// IngressCandidate returns the acceptor for mirrored requests to the candidate service.
func (e *Engine) IngressCandidate(protocol javascript.Protocol) Acceptor {
	return newIngressCandidate(e.ingress, protocol)
}

// EgressReference returns the acceptor for the reference's dependency requests.
func (e *Engine) EgressReference(protocol javascript.Protocol) Acceptor {
	return newEgressReference(e.egress, protocol)
}

// EgressCandidate returns the acceptor for the candidate's dependency requests.
func (e *Engine) EgressCandidate(protocol javascript.Protocol) Acceptor {
	return newEgressCandidate(e.egress, protocol)
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

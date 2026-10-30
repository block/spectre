package protocol

import (
	"context"
	"fmt"

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

// NewEngine loads the scripts and passes each declaration to the matcher for its
// protocol. A script naming an unregistered protocol fails startup.
func NewEngine(
	ctx context.Context,
	config Config,
	matchers map[model.Protocol]Matcher,
	quarantine Quarantiner,
) (*Engine, error) {
	if quarantine == nil {
		return nil, errors.New("quarantiner is required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	scripts, err := comparison.New(ctx, config.Comparison)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	for _, endpoint := range scripts.Endpoints() {
		if err := declare(matchers, endpoint.Protocol(), DeclarationRequest{Pattern: endpoint.Pattern(), Type: endpoint.Type()}); err != nil {
			return nil, err
		}
	}
	for _, rule := range scripts.Rules() {
		if err := declare(matchers, rule.Protocol(), DeclarationRule{Rule: rule}); err != nil {
			return nil, err
		}
	}
	return &Engine{
		schema:  scripts.Schema(),
		scripts: scripts,
		ingress: newIngressPairs(scripts, quarantine),
		egress:  newEgressQueue(scripts, config.MatchWindow, quarantine),
	}, nil
}

func declare(matchers map[model.Protocol]Matcher, protocol model.Protocol, declaration Declaration) error {
	matcher, registered := matchers[protocol]
	if !registered {
		return errors.Errorf("%s names unregistered protocol %q", describeDeclaration(declaration), protocol)
	}
	return errors.Wrapf(matcher.Declare(declaration), "declare %s", describeDeclaration(declaration))
}

func describeDeclaration(declaration Declaration) string {
	switch declared := declaration.(type) {
	case DeclarationRequest:
		return fmt.Sprintf("egress endpoint %q", declared.Pattern)
	case DeclarationRule:
		return "ingress rule " + declared.Rule.String()
	}
	return fmt.Sprintf("declaration %T", declaration)
}

// SelectRule returns the first of rules that selects a response.
func (e *Engine) SelectRule(ctx context.Context, rules []javascript.Rule, request javascript.Request, response javascript.Response) (Option[javascript.Rule], error) {
	return errors.WithStack2(e.scripts.SelectRule(ctx, rules, request, response))
}

// Schema returns the types that servers decode messages as.
func (e *Engine) Schema() *schema.Schema {
	return e.schema
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

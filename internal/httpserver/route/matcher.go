package route

import (
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/schema"
)

// Matcher holds the HTTP endpoints and rules that scripts declare. The engine
// declares every one before serving starts; matching is then safe for concurrent use.
type Matcher struct {
	requests *Map[Request]
	declared []Request
	rules    []declaredRule
}

// Request is one declared egress endpoint.
type Request struct {
	Pattern string
	Type    schema.TypeRef
}

// declaredRule is an ingress rule, with its pattern if it selects requests by one.
type declaredRule struct {
	rule    javascript.Rule
	pattern Option[*Pattern]
}

var _ protocol.Matcher = (*Matcher)(nil)

// NewMatcher returns a matcher with no endpoints.
func NewMatcher() *Matcher {
	return &Matcher{requests: New[Request]()}
}

// Declare adds an endpoint or a rule. Egress request patterns name a host; ingress rule patterns do not.
func (m *Matcher) Declare(declaration protocol.Declaration) error {
	switch declared := declaration.(type) {
	case protocol.DeclarationRequest:
		request := Request{Pattern: declared.Pattern, Type: declared.Type}
		if err := m.requests.Add(declared.Pattern, request); err != nil {
			return err
		}
		m.declared = append(m.declared, request)
		return nil
	case protocol.DeclarationRule:
		rule := declaredRule{rule: declared.Rule}
		if selector, ok := declared.Rule.Request().(javascript.RequestPattern); ok {
			pattern, err := NewPattern(selector.Pattern)
			if err != nil {
				return err
			}
			rule.pattern = Some(pattern)
		}
		m.rules = append(m.rules, rule)
		return nil
	}
	return errors.Errorf("unsupported declaration %T", declaration)
}

// Requests returns the declared egress endpoints in declaration order.
func (m *Matcher) Requests() []Request {
	return append([]Request(nil), m.declared...)
}

// Request returns the endpoint a dependency request matches and its path
// wildcards. requestPath must be escaped.
func (m *Matcher) Request(requestMethod, requestHost, requestPath string) (request Request, wildcards map[string]string, matched bool) {
	return m.requests.Match(requestMethod, requestHost, requestPath)
}

// Responses returns, in order, the rules that may select an inbound request's
// responses, or why every response is ignored. requestPath must be escaped.
func (m *Matcher) Responses(requestMethod, requestPath string) (rules []javascript.Rule, ignored Option[string]) {
	for _, declared := range m.rules {
		pattern, patterned := declared.pattern.Get()
		// Ingress rules never name a host.
		if patterned && !pattern.Match(requestMethod, "", requestPath) {
			continue
		}
		rules = append(rules, declared.rule)
		if _, every := declared.rule.Status().(javascript.StatusAny); !patterned || !every {
			continue
		}
		// This rule selects every remaining response, so later rules never apply.
		if _, ignore := declared.rule.Action().(javascript.ActionIgnore); ignore && len(rules) == 1 {
			return nil, Some("rule " + declared.rule.String() + " ignores the request")
		}
		return rules, None[string]()
	}
	return rules, None[string]()
}

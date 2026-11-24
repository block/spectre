package route

import (
	"net/http"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/protocols/http/script"
	"github.com/block/spectre/internal/schema"
)

// Matcher holds the HTTP endpoints and rules that scripts declare. It is built once
// from the loaded scripts, so matching is safe for concurrent use.
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
	rule    script.Rule
	pattern Option[*Pattern]
}

// NewMatcher declares the HTTP endpoints among endpoints and every rule. Egress request
// patterns name a host; ingress rule patterns do not. An invalid pattern is an error.
func NewMatcher(endpoints []javascript.Endpoint, rules []script.Rule) (*Matcher, error) {
	m := &Matcher{requests: New[Request]()}
	for _, endpoint := range endpoints {
		if endpoint.Protocol() != script.Protocol {
			continue
		}
		request := Request{Pattern: endpoint.Pattern(), Type: endpoint.Type()}
		if err := m.requests.Add(request.Pattern, request); err != nil {
			return nil, errors.Wrapf(err, "declare egress endpoint %q", request.Pattern)
		}
		m.declared = append(m.declared, request)
	}
	for _, rule := range rules {
		declared := declaredRule{rule: rule}
		if selector, ok := rule.Request().(script.RequestPattern); ok {
			pattern, err := NewPattern(selector.Pattern)
			if err != nil {
				return nil, errors.Wrapf(err, "declare ingress rule %s", rule)
			}
			declared.pattern = Some(pattern)
		}
		m.rules = append(m.rules, declared)
	}
	return m, nil
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
func (m *Matcher) Responses(requestMethod, requestPath string, header http.Header) (rules []script.Rule, ignored Option[string]) {
	for _, declared := range m.rules {
		pattern, patterned := declared.pattern.Get()
		// Ingress rules never name a host.
		if patterned && !pattern.Match(requestMethod, "", requestPath) || !declared.rule.SelectsRequestHeaders(header) {
			continue
		}
		rules = append(rules, declared.rule)
		if !patterned || !declared.rule.SelectsEveryResponse() {
			continue
		}
		// This rule selects every remaining response, so later rules never apply.
		if _, ignore := declared.rule.Action().(script.ActionIgnore); ignore && len(rules) == 1 {
			return nil, Some("rule " + declared.rule.String() + " ignores the request")
		}
		return rules, None[string]()
	}
	return rules, None[string]()
}

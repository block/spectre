package route

import (
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/schema"
)

// Matcher holds the HTTP endpoints that scripts declare. The engine declares
// every endpoint before serving starts; matching is then safe for concurrent use.
type Matcher struct {
	requests  *Map[Request]
	responses *Map[responseEndpoint]
	declared  []Request
}

// Request is one declared egress endpoint.
type Request struct {
	Pattern string
	Type    schema.TypeRef
}

// responseEndpoint is one declared ingress endpoint, either compared or ignored.
type responseEndpoint struct {
	ref     schema.TypeRef
	ignored Option[string]
}

var _ protocol.Matcher = (*Matcher)(nil)

// NewMatcher returns a matcher with no endpoints.
func NewMatcher() *Matcher {
	return &Matcher{requests: New[Request](), responses: New[responseEndpoint]()}
}

// Declare adds an endpoint. Egress request patterns name a host; ingress patterns do not.
func (m *Matcher) Declare(pattern string, declaration protocol.Declaration) error {
	switch declared := declaration.(type) {
	case protocol.DeclarationRequest:
		request := Request{Pattern: pattern, Type: declared.Type}
		if err := m.requests.Add(pattern, request); err != nil {
			return err
		}
		m.declared = append(m.declared, request)
		return nil
	case protocol.DeclarationResponse:
		return m.responses.Add(pattern, responseEndpoint{ref: declared.Type})
	case protocol.DeclarationIgnore:
		return m.responses.Add(pattern, responseEndpoint{ignored: Some(declared.Reason)})
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

// Response returns the response type for an inbound request, or why its
// responses are ignored. requestPath must be escaped.
func (m *Matcher) Response(requestMethod, requestPath string) (ref schema.TypeRef, ignored Option[string], matched bool) {
	// Ingress endpoints never name a host.
	endpoint, _, matched := m.responses.Match(requestMethod, "", requestPath)
	return endpoint.ref, endpoint.ignored, matched
}

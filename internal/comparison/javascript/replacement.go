package javascript

import (
	"fmt"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// The method names that a replacement is declared with.
const (
	replacesMethod = "replaces"
	usingMethod    = "using"
)

// Replacement declares that the candidate's requests to an endpoint replace the
// reference's requests to a source endpoint, and answers them from its responses.
type Replacement struct {
	index          int
	protocol       model.Protocol
	pattern        string
	response       schema.TypeRef
	source         string
	sourceRequest  schema.TypeRef
	sourceResponse schema.TypeRef
}

// NewReplacement creates the replacement of source's requests by pattern's, for a binding
// to declare through Scope.DeclareReplacement, which gives it its index.
func NewReplacement(protocol model.Protocol, pattern string, response schema.TypeRef, source string, sourceRequest, sourceResponse schema.TypeRef) Replacement {
	return Replacement{
		protocol: protocol, pattern: pattern, response: response,
		source: source, sourceRequest: sourceRequest, sourceResponse: sourceResponse,
	}
}

// atIndex returns the replacement at index among the scripts' replacements.
func (r Replacement) atIndex(index int) Replacement {
	r.index = index
	return r
}

// Index returns the replacement's position among the scripts' replacements.
func (r Replacement) Index() int {
	return r.index
}

// Protocol returns the protocol key that interprets both patterns.
func (r Replacement) Protocol() model.Protocol {
	return r.protocol
}

// Pattern returns the pattern of the endpoint whose requests are answered.
func (r Replacement) Pattern() string {
	return r.pattern
}

// Response returns the type of the answers.
func (r Replacement) Response() schema.TypeRef {
	return r.response
}

// Source returns the pattern of the endpoint whose reference responses answer requests.
func (r Replacement) Source() string {
	return r.source
}

// SourceRequest returns the type a request maps to, to pair with a source request.
func (r Replacement) SourceRequest() schema.TypeRef {
	return r.sourceRequest
}

// SourceResponse returns the type the source's reference responses decode as.
func (r Replacement) SourceResponse() schema.TypeRef {
	return r.sourceResponse
}

// String names the replacement the way a script declares it.
func (r Replacement) String() string {
	return fmt.Sprintf("%s(%q).%s(%q).%s()", r.protocol, r.pattern, replacesMethod, r.source, usingMethod)
}

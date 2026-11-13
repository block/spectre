package javascript

import (
	"fmt"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// Derivation answers the candidate's requests to an endpoint from the reference's
// responses to a source endpoint, instead of from the reference's own requests to it.
type Derivation struct {
	index          int
	protocol       model.Protocol
	pattern        string
	response       schema.TypeRef
	source         string
	sourceRequest  schema.TypeRef
	sourceResponse schema.TypeRef
}

// newDerivation creates the derivation at index of pattern's requests from source's responses.
func newDerivation(index int, pattern string, response schema.TypeRef, source string, sourceRequest, sourceResponse schema.TypeRef) Derivation {
	return Derivation{
		index: index, protocol: model.ProtocolHTTP, pattern: pattern, response: response,
		source: source, sourceRequest: sourceRequest, sourceResponse: sourceResponse,
	}
}

// Index returns the derivation's position among the scripts' derivations.
func (d Derivation) Index() int {
	return d.index
}

// Protocol returns the protocol key that interprets both patterns.
func (d Derivation) Protocol() model.Protocol {
	return d.protocol
}

// Pattern returns the pattern of the endpoint whose requests are answered.
func (d Derivation) Pattern() string {
	return d.pattern
}

// Response returns the type of the answers.
func (d Derivation) Response() schema.TypeRef {
	return d.response
}

// Source returns the pattern of the endpoint whose reference responses answer requests.
func (d Derivation) Source() string {
	return d.source
}

// SourceRequest returns the type a request maps to, to pair with a source request.
func (d Derivation) SourceRequest() schema.TypeRef {
	return d.sourceRequest
}

// SourceResponse returns the type the source's reference responses decode as.
func (d Derivation) SourceResponse() schema.TypeRef {
	return d.sourceResponse
}

// String names the derivation the way a script declares it.
func (d Derivation) String() string {
	return fmt.Sprintf("%s(%q).%s().%s(%q)", d.protocol, d.pattern, deriveMethod, fromMethod, d.source)
}

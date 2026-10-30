package javascript

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// Rule decides how ingress compares the responses it selects. Rules apply in
// declaration order, and the first that selects a response decides.
type Rule struct {
	index    int
	protocol model.Protocol
	request  RequestSelector
	status   StatusSelector
	action   Action
}

func newRule(index int, protocol model.Protocol, request RequestSelector, status StatusSelector, action Action) Rule {
	return Rule{index: index, protocol: protocol, request: request, status: status, action: action}
}

// Index returns the rule's position among the scripts' rules.
func (r Rule) Index() int {
	return r.index
}

// Protocol returns the protocol key that interprets the rule.
func (r Rule) Protocol() model.Protocol {
	return r.protocol
}

// Request returns how the rule selects requests.
func (r Rule) Request() RequestSelector {
	return r.request
}

// Status returns how the rule selects responses by status.
func (r Rule) Status() StatusSelector {
	return r.status
}

// Action returns what happens to the responses the rule selects.
func (r Rule) Action() Action {
	return r.action
}

// String names the rule the way a script declares it.
func (r Rule) String() string {
	var text strings.Builder
	switch request := r.request.(type) {
	case RequestPattern:
		fmt.Fprintf(&text, "%s(%q)", r.protocol, request.Pattern)
	case RequestScript:
		fmt.Fprintf(&text, "%s(<function>)", r.protocol)
	}
	switch status := r.status.(type) {
	case StatusAny:
	case StatusCodes:
		codes := make([]string, 0, len(status.Codes))
		for _, code := range status.Codes {
			codes = append(codes, strconv.Itoa(code))
		}
		fmt.Fprintf(&text, ".status(%s)", strings.Join(codes, ", "))
	case StatusScript:
		text.WriteString(".status(<function>)")
	}
	return text.String()
}

// sameSelectors reports whether two rules select exactly the same responses
// without script functions.
func (r Rule) sameSelectors(other Rule) bool {
	pattern, ok := r.request.(RequestPattern)
	otherPattern, otherOK := other.request.(RequestPattern)
	if !ok || !otherOK || r.protocol != other.protocol || pattern != otherPattern {
		return false
	}
	switch status := r.status.(type) {
	case StatusAny:
		_, same := other.status.(StatusAny)
		return same
	case StatusCodes:
		otherStatus, same := other.status.(StatusCodes)
		return same && slices.Equal(status.Codes, otherStatus.Codes)
	case StatusScript:
		return false
	}
	return false
}

// RequestSelector selects the requests a rule applies to.
//
//sumtype:decl
type RequestSelector interface{ requestSelector() }

// RequestPattern selects requests matching a ServeMux pattern.
type RequestPattern struct{ Pattern string }

// RequestScript selects the requests a script function accepts.
type RequestScript struct{}

func (RequestPattern) requestSelector() {}
func (RequestScript) requestSelector()  {}

// StatusSelector selects the responses a rule applies to by their status.
//
//sumtype:decl
type StatusSelector interface{ statusSelector() }

// StatusAny selects every response.
type StatusAny struct{}

// StatusCodes selects responses with one of the codes.
type StatusCodes struct{ Codes []int }

// StatusScript selects the responses whose status a script function accepts.
type StatusScript struct{}

func (StatusAny) statusSelector()    {}
func (StatusCodes) statusSelector()  {}
func (StatusScript) statusSelector() {}

// Action is what happens to the responses a rule selects.
//
//sumtype:decl
type Action interface{ action() }

// ActionIgnore skips comparison, so the responses never quarantine.
type ActionIgnore struct{}

// ActionMatch decodes bodies as Type, or as text when Type is None, then
// compares them after the rule's normaliser, if it has one.
type ActionMatch struct {
	Type       Option[schema.TypeRef]
	Normalises bool
}

func (ActionIgnore) action() {}
func (ActionMatch) action()  {}

// Request is an inbound HTTP request as a rule's request function sees it.
type Request struct {
	Method string
	// Path is escaped and excludes the query.
	Path string
}

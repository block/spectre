package javascript

import (
	"fmt"
	"net/http"
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
	// headers are sorted by name, and a response must satisfy every one.
	headers []HeaderSelector
	action  Action
}

func newRule(index int, protocol model.Protocol, request RequestSelector, status StatusSelector, headers []HeaderSelector, action Action) Rule {
	return Rule{index: index, protocol: protocol, request: request, status: status, headers: headers, action: action}
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

// Headers returns how the rule selects responses by their headers, sorted by name.
func (r Rule) Headers() []HeaderSelector {
	return slices.Clone(r.headers)
}

// SelectsEveryResponse reports whether the rule selects every response to the
// requests it selects.
func (r Rule) SelectsEveryResponse() bool {
	_, every := r.status.(StatusAny)
	return every && len(r.headers) == 0
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
	for _, header := range r.headers {
		switch header := header.(type) {
		case HeaderValue:
			fmt.Fprintf(&text, ".header(%q, %q)", header.Name, header.Value)
		case HeaderScript:
			fmt.Fprintf(&text, ".header(%q, <function>)", header.Name)
		}
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
		if _, same := other.status.(StatusAny); !same {
			return false
		}
	case StatusCodes:
		otherStatus, same := other.status.(StatusCodes)
		if !same || !slices.Equal(status.Codes, otherStatus.Codes) {
			return false
		}
	case StatusScript:
		return false
	}
	return slices.EqualFunc(r.headers, other.headers, func(header, otherHeader HeaderSelector) bool {
		value, fixed := header.(HeaderValue)
		return fixed && value == otherHeader
	})
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

// HeaderSelector selects the responses a rule applies to by one header.
//
//sumtype:decl
type HeaderSelector interface{ headerName() string }

// HeaderValue selects responses whose header, with its values joined by ", ",
// equals Value. It never selects a response without the header.
type HeaderValue struct{ Name, Value string }

// HeaderScript selects the responses whose header a script function accepts.
type HeaderScript struct{ Name string }

func (h HeaderValue) headerName() string  { return h.Name }
func (h HeaderScript) headerName() string { return h.Name }

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

// Response is an HTTP response as a rule's selectors see it.
type Response struct {
	Status int
	Header http.Header
}

// HeaderValue returns a header's values joined by ", ", or None without any.
func (r Response) HeaderValue(name string) Option[string] {
	values := r.Header.Values(name)
	if len(values) == 0 {
		return None[string]()
	}
	return Some(strings.Join(values, ", "))
}

// Request is an inbound HTTP request as a rule's request function sees it.
type Request struct {
	Method string
	// Path is escaped and excludes the query.
	Path string
}

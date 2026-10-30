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
	// requestHeaders and responseHeaders are sorted by name, and a rule selects
	// only responses that satisfy every one.
	requestHeaders  []HeaderSelector
	responseHeaders []HeaderSelector
	action          Action
}

func newRule(index int, protocol model.Protocol, request RequestSelector, status StatusSelector, requestHeaders, responseHeaders []HeaderSelector, action Action) Rule {
	return Rule{
		index: index, protocol: protocol, request: request, status: status,
		requestHeaders: requestHeaders, responseHeaders: responseHeaders, action: action,
	}
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

// RequestHeaders returns how the rule selects requests by their headers, sorted by name.
func (r Rule) RequestHeaders() []HeaderSelector {
	return slices.Clone(r.requestHeaders)
}

// ResponseHeaders returns how the rule selects responses by their headers, sorted by name.
func (r Rule) ResponseHeaders() []HeaderSelector {
	return slices.Clone(r.responseHeaders)
}

// SelectsRequestHeaders reports whether a request has the header values the
// rule requires. Header functions are left to the caller.
func (r Rule) SelectsRequestHeaders(header http.Header) bool {
	return !slices.ContainsFunc(r.requestHeaders, func(selector HeaderSelector) bool {
		value, fixed := selector.(HeaderValue)
		return fixed && !value.Selects(joinedHeader(header, value.Name))
	})
}

// SelectsEveryResponse reports whether the rule selects every response to a
// request its pattern and request header values select.
func (r Rule) SelectsEveryResponse() bool {
	_, every := r.status.(StatusAny)
	scripted := slices.ContainsFunc(r.requestHeaders, func(selector HeaderSelector) bool {
		_, isScript := selector.(HeaderScript)
		return isScript
	})
	return every && !scripted && len(r.responseHeaders) == 0
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
	writeHeaders(&text, requestHeaderMethod, r.requestHeaders)
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
	writeHeaders(&text, responseHeaderMethod, r.responseHeaders)
	return text.String()
}

func writeHeaders(text *strings.Builder, method string, headers []HeaderSelector) {
	for _, header := range headers {
		switch header := header.(type) {
		case HeaderValue:
			fmt.Fprintf(text, ".%s(%q, %q)", method, header.Name, header.Value)
		case HeaderScript:
			fmt.Fprintf(text, ".%s(%q, <function>)", method, header.Name)
		}
	}
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
	return sameHeaderValues(r.requestHeaders, other.requestHeaders) && sameHeaderValues(r.responseHeaders, other.responseHeaders)
}

// sameHeaderValues reports whether two sorted header selector lists require the
// same values and have no functions.
func sameHeaderValues(headers, other []HeaderSelector) bool {
	return slices.EqualFunc(headers, other, func(header, otherHeader HeaderSelector) bool {
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

// HeaderSelector selects the requests or responses a rule applies to by one header.
//
//sumtype:decl
type HeaderSelector interface{ headerName() string }

// HeaderValue selects messages whose header, with its values joined by ", ",
// equals Value. It never selects a message without the header.
type HeaderValue struct{ Name, Value string }

// Selects reports whether a header's joined value equals Value.
func (h HeaderValue) Selects(value Option[string]) bool {
	present, ok := value.Get()
	return ok && present == h.Value
}

// HeaderScript selects the messages whose header a script function accepts.
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
	return joinedHeader(r.Header, name)
}

// Request is an inbound HTTP request as a rule's selectors see it.
type Request struct {
	Method string
	// Path is escaped and excludes the query.
	Path   string
	Header http.Header
}

// HeaderValue returns a header's values joined by ", ", or None without any.
func (r Request) HeaderValue(name string) Option[string] {
	return joinedHeader(r.Header, name)
}

func joinedHeader(header http.Header, name string) Option[string] {
	values := header.Values(name)
	if len(values) == 0 {
		return None[string]()
	}
	return Some(strings.Join(values, ", "))
}

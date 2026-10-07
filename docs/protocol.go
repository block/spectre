package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/types/optional"
	"go.opentelemetry.io/otel/baggage"

	"github.com/block/spectre/internal/route"
)

// TypeRef is a dot-delimited TypeScript schema type, e.g. weather.service.GetWeatherRequest.
type TypeRef string

// RequestID links operations to the mirrored inbound request that caused them.
// An egress request without one quarantines the candidate.
type RequestID string

// Route is a protocol's canonical request route, e.g. "GET forecasts.example/v1/forecasts/london".
// Equal routes must have identical text, as pairing keys hash it.
type Route string

// Exchange links a forwarded request to its response.
type Exchange struct {
	// id is assigned by the Engine and unique among its in-flight forwarded requests.
	id uint64
}

// Decision is returned by the Engine to tell the server what action to take.
type Decision interface{ decision() }

// DecisionIntercept informs a server to intercept the request and return the given response.
type DecisionIntercept struct{ Response any }

func (DecisionIntercept) decision() {}

// DecisionForward informs a server to forward the request and report the response with Exchange.
// The server propagates RequestID if present, and otherwise strips it, e.g. before a dependency.
type DecisionForward struct {
	RequestID optional.Option[RequestID]
	Exchange  Exchange
}

func (DecisionForward) decision() {}

// Protocol is the protocol of the server.
type Protocol string

const (
	ProtocolHTTP  Protocol = "http"
	ProtocolMySQL Protocol = "mysql"
)

// Matcher owns a protocol's route table. The engine passes it the script declarations for
// its protocol while scripts load, so invalid patterns fail startup.
type Matcher interface {
	Declare(pattern string, ref TypeRef) error
}

// Engine owns the state shared by every Acceptor.
type Engine struct {
	matchers map[Protocol]Matcher
}

// Register adds a protocol's matcher. Scripts naming an unregistered protocol fail to load.
func (e *Engine) Register(protocol Protocol, matcher Matcher) error {
	panic("??")
}

// Acceptor drives the decisions for one ingress or egress point.
type Acceptor struct {
	engine   *Engine
	protocol Protocol
}

// NewAcceptor creates an Acceptor for one point, sharing the engine's state.
func NewAcceptor(engine *Engine, protocol Protocol) *Acceptor {
	return &Acceptor{engine: engine, protocol: protocol}
}

// ProcessRequest is called when a request is received at this point.
// ref is the type the scripts declared for the route, if any.
func (a *Acceptor) ProcessRequest(
	ctx context.Context,
	requestID optional.Option[RequestID],
	route Route,
	ref optional.Option[TypeRef],
	request any,
) (decision Decision, err error) {
	panic("??")
}

// ProcessResponse is called with the response to a request that was forwarded.
// ref is only needed when the server knows the response type itself, e.g. from a gRPC method.
func (a *Acceptor) ProcessResponse(
	ctx context.Context,
	exchange Exchange,
	ref optional.Option[TypeRef],
	response any,
) (err error) {
	panic("??")
}

// requestIDKey is the W3C baggage entry carrying the RequestID.
const requestIDKey = "spectre_request_id"

// requestIDFromBaggage treats malformed baggage as missing context.
func requestIDFromBaggage(header http.Header) optional.Option[RequestID] {
	bag, err := baggage.Parse(strings.Join(header.Values("Baggage"), ","))
	if err != nil {
		return optional.None[RequestID]()
	}
	if value := bag.Member(requestIDKey).Value(); value != "" {
		return optional.Some(RequestID(value))
	}
	return optional.None[RequestID]()
}

// setRequestIDBaggage replaces only Spectre's entry, preserving other baggage.
func setRequestIDBaggage(header http.Header, requestID optional.Option[RequestID]) error {
	bag, err := baggage.Parse(strings.Join(header.Values("Baggage"), ","))
	if err != nil {
		return errors.Wrap(err, "parse baggage")
	}
	bag = bag.DeleteMember(requestIDKey)
	if id, ok := requestID.Get(); ok {
		member, err := baggage.NewMemberRaw(requestIDKey, string(id))
		if err != nil {
			return errors.Wrap(err, "encode request ID baggage")
		}
		if bag, err = bag.SetMember(member); err != nil {
			return errors.Wrap(err, "set request ID baggage")
		}
	}
	header.Del("Baggage")
	if bag.Len() > 0 {
		header.Set("Baggage", bag.String())
	}
	return nil
}

type HTTPServer struct {
	acceptor  *Acceptor
	transport http.RoundTripper
	// routes is built by Declare before serving, then only read.
	routes *route.Map[TypeRef]
}

var _ Matcher = (*HTTPServer)(nil)

func NewHTTPServer(acceptor *Acceptor, transport http.RoundTripper) *HTTPServer {
	return &HTTPServer{acceptor: acceptor, transport: transport, routes: route.New[TypeRef]()}
}

func (s *HTTPServer) Declare(pattern string, ref TypeRef) error {
	return errors.Wrapf(s.routes.Add(pattern, ref), "declare %q", pattern)
}

// httpRoute lowercases the host and drops its port, so equivalent requests share a Route.
func httpRoute(r *http.Request) Route {
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return Route(r.Method + " " + strings.ToLower(host) + r.URL.EscapedPath())
}

func (s *HTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body any
	if err := json.Unmarshal(data, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ref := optional.None[TypeRef]()
	// Binding the returned wildcards into body is omitted from this sketch.
	if matched, _, ok := s.routes.Match(r.Method, r.Host, r.URL.EscapedPath()); ok {
		ref = optional.Some(matched)
	}
	decision, err := s.acceptor.ProcessRequest(ctx, requestIDFromBaggage(r.Header), httpRoute(r), ref, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	switch decision := decision.(type) {
	case DecisionForward:
		s.forward(w, r, data, decision)

	case DecisionIntercept:
		if err := json.NewEncoder(w).Encode(decision.Response); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func (s *HTTPServer) forward(w http.ResponseWriter, r *http.Request, data []byte, decision DecisionForward) {
	ctx := r.Context()
	outbound := r.Clone(ctx)
	outbound.RequestURI = ""
	outbound.Body = io.NopCloser(bytes.NewReader(data))
	if err := setRequestIDBaggage(outbound.Header, decision.RequestID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	response, err := s.transport.RoundTrip(outbound)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	responseData, err := io.ReadAll(response.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	var responseBody any
	if err := json.Unmarshal(responseData, &responseBody); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := s.acceptor.ProcessResponse(ctx, decision.Exchange, optional.None[TypeRef](), responseBody); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for name, values := range response.Header {
		w.Header()[name] = values
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseData)
}

package httpserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/httpserver/message"
	"github.com/block/spectre/internal/httpserver/middleware/health"
	"github.com/block/spectre/internal/httpserver/middleware/logging"
	"github.com/block/spectre/internal/httpserver/route"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/proxy"
)

// Egress serves reference and candidate traffic on separate listeners. Candidate
// requests never reach a dependency under any condition.
type Egress struct {
	config           EgressConfig
	transport        http.RoundTripper
	engine           *protocol.Engine
	routes           *route.Matcher
	destinations     map[string]*httputil.ReverseProxy
	listeners        []*netaddr.Endpoint
	candidates       *proxy.Candidates
	requests         chan struct{}
	decoder          *message.Decoder
	referencePoint   *point
	candidatePoint   *point
	referenceHandler http.Handler
	candidateHandler http.Handler
	health           *health.Handler
}

// NewEgress constructs an egress proxy and the engine it owns.
func NewEgress(
	ctx context.Context,
	config EgressConfig,
	engineConfig protocol.Config,
	transport http.RoundTripper,
) (*Egress, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("transport is required")
	}
	candidateListen := netaddr.ParseListen(config.CandidateListen)
	if !candidateListen.IsUnix() && !candidateListen.IsLoopback() {
		return nil, errors.New("candidate listener must use a loopback IP address or a unix socket")
	}
	listeners := []*netaddr.Endpoint{
		netaddr.ParseListen(config.ReferenceListen),
		candidateListen,
		netaddr.ParseListen(config.HealthListen),
	}
	destinations, err := egressDestinations(ctx, config, transport, listeners)
	if err != nil {
		return nil, err
	}
	static, err := descriptors.LoadDescriptorSets(config.Descriptors)
	if err != nil {
		return nil, errors.Wrap(err, "load static descriptors")
	}
	candidates := proxy.NewCandidates(config.Candidates)
	routes := route.NewMatcher()
	engine, err := protocol.NewEngine(ctx, engineConfig, map[boundary.Protocol]protocol.Matcher{boundary.ProtocolHTTP: routes}, candidates)
	if err != nil {
		return nil, errors.Wrap(err, "configure request hashing")
	}
	decoder, err := message.New(engine.Schema(), static, routes, config.ComparisonMaxBodyBytes)
	if err != nil {
		return nil, errors.Wrap(err, "configure request decoding")
	}
	// Recordings wait to be replayed, so all of them share one budget.
	budget := proxy.NewBudget(config.RecordingBufferBytes)
	recordingBudget := func() *proxy.Budget { return budget }
	egress := &Egress{
		config:         config,
		transport:      transport,
		engine:         engine,
		routes:         routes,
		destinations:   destinations,
		listeners:      listeners,
		candidates:     candidates,
		requests:       make(chan struct{}, config.MaxInFlightRequests),
		decoder:        decoder,
		referencePoint: newPoint(engine.EgressReference(boundary.ProtocolHTTP), recordingBudget, false, true),
		candidatePoint: newPoint(engine.EgressCandidate(boundary.ProtocolHTTP), recordingBudget, false, true),
		health:         health.New(http.NotFoundHandler()),
	}
	egress.referenceHandler = logging.New(http.HandlerFunc(egress.serveReference), logger.EventReceived)
	egress.candidateHandler = logging.New(http.HandlerFunc(egress.serveCandidate), logger.EventReceived)
	return egress, nil
}

func egressDestinations(ctx context.Context, config EgressConfig, transport http.RoundTripper,
	listeners []*netaddr.Endpoint,
) (map[string]*httputil.ReverseProxy, error) {
	destinations := make(map[string]*httputil.ReverseProxy, len(config.Destinations))
	for host, upstream := range config.Destinations {
		name := hostname(host)
		if name == "" || name != strings.ToLower(host) {
			return nil, errors.Errorf("destination host %q must be a host name without a port", host)
		}
		if _, duplicate := destinations[name]; duplicate {
			return nil, errors.Errorf("destination host %q is configured more than once", host)
		}
		endpoint, err := netaddr.ParseBackend(upstream)
		if err != nil {
			return nil, errors.Wrapf(err, "parse destination %q", host)
		}
		if targetsListener(endpoint, listeners) {
			return nil, errors.Errorf("destination %q must not target an egress listener", host)
		}
		destination, err := NewReverseProxy(ctx, endpoint, transport, false)
		if err != nil {
			return nil, errors.Wrapf(err, "proxy destination %q", host)
		}
		destinations[name] = destination
	}
	return destinations, nil
}

// Serve accepts reference, candidate, and health traffic until the context is
// cancelled or a server fails. Readiness turns true once the servers start.
func (e *Egress) Serve(ctx context.Context, reference net.Listener, candidate net.Listener, probes net.Listener) error {
	bindings := []Binding{
		NewBinding(reference, e.referenceHandler),
		NewBinding(candidate, e.candidateHandler),
		NewBinding(probes, e.health),
	}
	lifecycle := NewLifecycle(e.markReady, e.stop, e.drain)
	return errors.Wrap(Serve(ctx, e.config.Config, bindings, lifecycle), "serve egress")
}

// Candidates reports the egress candidate quarantine state.
func (e *Egress) Candidates() *proxy.Candidates {
	return e.candidates
}

func (e *Egress) markReady(context.Context) {
	e.health.SetReady(true)
}

// stop cancels waiting candidates before servers shut down, because their
// handlers would otherwise hold shutdown for up to the match window.
func (e *Egress) stop() {
	e.health.SetReady(false)
	e.candidates.Close()
}

func (e *Egress) drain(ctx context.Context) error {
	if err := e.candidates.Shutdown(ctx); err != nil {
		return errors.WithStack(err)
	}
	return errors.Wrap(e.engine.Shutdown(ctx), "wait for reference hashing")
}

// serveReference forwards immediately. The engine identifies a copy of the
// request in parallel, so a failure to identify it never affects the reference.
func (e *Egress) serveReference(writer http.ResponseWriter, request *http.Request) {
	select {
	case e.requests <- struct{}{}:
		defer func() { <-e.requests }()
	default:
		http.Error(writer, "egress request capacity exceeded", http.StatusServiceUnavailable)
		return
	}
	ctx := request.Context()
	upstream := e.upstream(request)
	// Snapshot the request now, because the engine may decode it after the handler returns.
	snapshot := newMessageRequest(request)
	captured := newRequestCapture(e.config.ComparisonMaxBodyBytes)
	outbound := request.Clone(ctx)
	switch {
	case !upstream.Ok():
		captured.closeWriter(errors.New("request has no usable destination"))
	case request.Body == nil || request.Body == http.NoBody || request.ContentLength == 0:
		// The reverse proxy drops empty bodies without closing them.
		captured.closeWriter(io.EOF)
	default:
		outbound.Body = newTeeBody(request.Body, captured)
	}
	payload := func(ctx context.Context) (protocol.Message, error) {
		if _, configured := e.destinations[hostname(snapshot.Host)]; !configured {
			return protocol.Message{}, errors.Errorf("reference request to unconfigured host %q", snapshot.Host)
		}
		body, overflow, err := captured.wait(ctx)
		if err != nil {
			return protocol.Message{}, err
		}
		snapshot.Body, snapshot.Overflow = body, overflow
		return errors.WithStack2(e.decoder.Request(snapshot))
	}
	decision, err := e.referencePoint.process(ctx, None[protocol.RequestID](), e.route(request), None[string](), payload)
	if err != nil {
		logger.FromContext(ctx).ErrorContext(ctx, "Reference request was not recorded", "error", err)
		if destination, ok := upstream.Get(); ok {
			destination.ServeHTTP(writer, outbound)
			return
		}
		writer.WriteHeader(http.StatusBadGateway)
		return
	}
	e.referencePoint.respond(writer, outbound, decision, upstream, noDecode)
}

// upstream returns the destination for a request's host. Other hosts are reached
// over plaintext HTTP as received, unless they would loop back to egress.
func (e *Egress) upstream(request *http.Request) Option[*httputil.ReverseProxy] {
	if destination, configured := e.destinations[hostname(request.Host)]; configured {
		return Some(destination)
	}
	endpoint, err := netaddr.ParseBackend("http://" + request.Host)
	if err == nil && targetsListener(endpoint, e.listeners) {
		err = errors.New("host targets an egress listener")
	}
	var destination *httputil.ReverseProxy
	if err == nil {
		destination, err = NewReverseProxy(request.Context(), endpoint, e.transport, false)
	}
	if err != nil {
		logger.FromContext(request.Context()).ErrorContext(request.Context(), "Reference request has no usable destination", "host", request.Host, "error", err)
		return None[*httputil.ReverseProxy]()
	}
	return Some(destination)
}

// serveCandidate replays the matching reference response. The engine quarantines
// the candidate when no reference matches, unless the candidate itself gave up.
func (e *Egress) serveCandidate(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	run, admitted := e.candidates.Start(ctx, cancel).Get()
	if !admitted {
		http.Error(writer, "candidate requests are refused", http.StatusServiceUnavailable)
		return
	}
	defer e.candidates.Finish(run)
	payload := func(context.Context) (protocol.Message, error) {
		if _, configured := e.destinations[hostname(request.Host)]; !configured {
			return protocol.Message{}, errors.Errorf("candidate request to unconfigured host %q", request.Host)
		}
		limit := e.config.ComparisonMaxBodyBytes
		body, err := io.ReadAll(io.LimitReader(request.Body, int64(limit)+1))
		if err != nil {
			return protocol.Message{}, errors.Wrap(err, "read candidate request body")
		}
		captured := newMessageRequest(request)
		captured.Body, captured.Overflow = body[:min(len(body), limit)], len(body) > limit
		return errors.WithStack2(e.decoder.Request(captured))
	}
	decision, err := e.candidatePoint.process(ctx, None[protocol.RequestID](), e.route(request), None[string](), payload)
	if err != nil {
		http.Error(writer, "no matching reference response", http.StatusBadGateway)
		return
	}
	e.candidatePoint.respond(writer, request, decision, None[*httputil.ReverseProxy](), noDecode)
}

// route names a request by the endpoint pattern it matches, so requests that
// differ only in path wildcards pair. Others are named by host, ignoring any port, and path.
func (e *Egress) route(request *http.Request) protocol.Route {
	if endpoint, _, declared := e.routes.Request(request.Method, request.Host, request.URL.EscapedPath()); declared {
		return protocol.Route(endpoint.Pattern)
	}
	return protocol.Route(request.Method + " " + hostname(request.Host) + request.URL.EscapedPath())
}

// newMessageRequest describes a request without its body, detached from the
// request's own header map.
func newMessageRequest(request *http.Request) message.Request {
	return message.Request{
		Method:   request.Method,
		Host:     request.Host,
		Path:     request.URL.EscapedPath(),
		RawQuery: request.URL.RawQuery,
		Header:   request.Header.Clone(),
	}
}

// hostname selects a destination by host name alone, ignoring any port.
func hostname(host string) string {
	return strings.ToLower((&url.URL{Host: host}).Hostname())
}

func targetsListener(endpoint *netaddr.Endpoint, listeners []*netaddr.Endpoint) bool {
	return slices.ContainsFunc(listeners, endpoint.TargetsListener)
}

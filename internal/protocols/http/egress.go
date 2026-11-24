package http

import (
	"context"
	"io"
	"maps"
	"net"
	nethttp "net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/gobwas/glob"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/httpservice"
	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/httpservice/logging"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/http/message"
	"github.com/block/spectre/internal/protocols/http/route"
	"github.com/block/spectre/internal/protocols/http/script"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/schema"
)

// Egress serves reference and candidate traffic on separate listeners. Candidate
// requests never reach a dependency under any condition.
type Egress struct {
	serve            httpservice.ServeConfig
	maxBodyBytes     int
	transport        nethttp.RoundTripper
	sourceResponse   func(route model.Route) Option[schema.TypeRef]
	routes           *route.Matcher
	destinations     []destination
	listeners        []*netaddr.Endpoint
	candidates       *proxy.Candidates
	requests         chan struct{}
	decoder          *message.Decoder
	referencePoint   *point
	candidatePoint   *point
	referenceHandler nethttp.Handler
	candidateHandler nethttp.Handler
	health           *health.Handler
}

// NewEgress constructs an egress proxy on the shared side that the mirror owns. The
// mirror closes and shuts the side down, and marks the proxy unready.
func NewEgress(
	ctx context.Context,
	config EgressConfig,
	handle script.Handle,
	runtime protocols.Runtime,
	side protocols.Side,
	transport nethttp.RoundTripper,
) (*Egress, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("transport is required")
	}
	if runtime.Scripts == nil || side.Candidates == nil || side.Health == nil || side.SourceResponse == nil {
		return nil, errors.New("scripts, candidates, health and source responses are required")
	}
	candidateListen := netaddr.ParseListen(config.CandidateListen)
	if !candidateListen.IsUnix() && !candidateListen.IsLoopback() {
		return nil, errors.New("candidate listener must use a loopback IP address or a unix socket")
	}
	listeners := []*netaddr.Endpoint{netaddr.ParseListen(config.ReferenceListen), candidateListen}
	destinations, err := egressDestinations(ctx, config.Destinations, transport, listeners)
	if err != nil {
		return nil, err
	}
	static, err := descriptors.LoadDescriptorSets(config.Descriptors)
	if err != nil {
		return nil, errors.Wrap(err, "load static descriptors")
	}
	routes, err := route.NewMatcher(runtime.Scripts.Endpoints(), handle.Declared(runtime.Scripts.Program()).Rules)
	if err != nil {
		return nil, errors.Wrap(err, "configure request hashing")
	}
	decoder, err := message.New(runtime.Scripts, handle, static, routes, config.ComparisonMaxBodyBytes)
	if err != nil {
		return nil, errors.Wrap(err, "configure request decoding")
	}
	// Recordings wait to be replayed, so all of them share one budget.
	budget := proxy.NewBudget(config.RecordingBufferBytes)
	recordingBudget := func() *proxy.Budget { return budget }
	egress := &Egress{
		serve:          config.ServeConfig(),
		maxBodyBytes:   config.ComparisonMaxBodyBytes,
		transport:      transport,
		sourceResponse: side.SourceResponse,
		routes:         routes,
		destinations:   destinations,
		listeners:      listeners,
		candidates:     side.Candidates,
		requests:       make(chan struct{}, config.MaxInFlightRequests),
		decoder:        decoder,
		referencePoint: newPoint(side.Reference, recordingBudget, false, true),
		candidatePoint: newPoint(side.Candidate, recordingBudget, false, true),
		health:         side.Health,
	}
	egress.referenceHandler = logging.New(nethttp.HandlerFunc(egress.serveReference), logger.EventReceived)
	egress.candidateHandler = logging.New(nethttp.HandlerFunc(egress.serveCandidate), logger.EventReceived)
	return egress, nil
}

// destination is the upstream of the request hosts its pattern matches.
type destination struct {
	pattern *glob.Pattern
	proxy   *httputil.ReverseProxy
}

// egressDestinations returns the configured destinations in host pattern order.
func egressDestinations(ctx context.Context, upstreams map[string]string, transport nethttp.RoundTripper,
	listeners []*netaddr.Endpoint,
) ([]destination, error) {
	destinations := make([]destination, 0, len(upstreams))
	seen := map[string]bool{}
	for _, host := range slices.Sorted(maps.Keys(upstreams)) {
		upstream := upstreams[host]
		name := strings.ToLower(host)
		if name == "" || strings.Contains(name, ":") {
			return nil, errors.Errorf("destination host %q must be a host name without a port", host)
		}
		if seen[name] {
			return nil, errors.Errorf("destination host %q is configured more than once", host)
		}
		seen[name] = true
		endpoint, err := netaddr.ParseBackend(upstream)
		if err != nil {
			return nil, errors.Wrapf(err, "parse destination %q", host)
		}
		if targetsListener(endpoint, listeners) {
			return nil, errors.Errorf("destination %q must not target an egress listener", host)
		}
		pattern, err := glob.Compile(name)
		if err != nil {
			return nil, errors.Wrapf(err, "destination host %q is not a valid glob", host)
		}
		proxy, err := NewReverseProxy(ctx, endpoint, transport, false)
		if err != nil {
			return nil, errors.Wrapf(err, "proxy destination %q", host)
		}
		destinations = append(destinations, destination{pattern: pattern, proxy: proxy})
	}
	return destinations, nil
}

// Serve accepts reference and candidate traffic until the context is cancelled or a
// server fails. Readiness turns true once the servers start.
func (e *Egress) Serve(ctx context.Context, reference net.Listener, candidate net.Listener) error {
	bindings := []httpservice.Binding{
		httpservice.NewBinding(reference, e.referenceHandler),
		httpservice.NewBinding(candidate, e.candidateHandler),
	}
	lifecycle := httpservice.NewLifecycle(e.markReady, func() {}, func(context.Context) error { return nil })
	return errors.Wrap(httpservice.Serve(ctx, e.serve, bindings, lifecycle), "serve egress")
}

// Health reports the egress readiness.
func (e *Egress) Health() *health.Handler {
	return e.health
}

func (e *Egress) markReady(context.Context) {
	e.health.SetReady(true)
}

// serveReference forwards immediately. The engine identifies a copy of the
// request in parallel, so a failure to identify it never affects the reference.
func (e *Egress) serveReference(writer nethttp.ResponseWriter, request *nethttp.Request) {
	select {
	case e.requests <- struct{}{}:
		defer func() { <-e.requests }()
	default:
		nethttp.Error(writer, "egress request capacity exceeded", nethttp.StatusServiceUnavailable)
		return
	}
	ctx := request.Context()
	// Taking the ID before the request is copied keeps it out of hashing.
	requestID, err := takeRequestID(request.Header)
	if err != nil {
		logger.FromContext(ctx).WarnContext(ctx, "Reference request is not paired", "error", err)
	}
	upstream := e.upstream(request)
	// Snapshot the request now, because the engine may decode it after the handler returns.
	snapshot := newMessageRequest(request)
	captured := newRequestCapture(e.maxBodyBytes)
	outbound := request.Clone(ctx)
	switch {
	case !upstream.Ok():
		captured.closeWriter(errors.New("request has no usable destination"))
	case request.Body == nil || request.Body == nethttp.NoBody || request.ContentLength == 0:
		// The reverse proxy drops empty bodies without closing them.
		captured.closeWriter(io.EOF)
	default:
		outbound.Body = newTeeBody(request.Body, captured)
	}
	payload := func(ctx context.Context) (schema.Value, error) {
		if _, configured := e.destination(snapshot.Host); !configured {
			return nil, errors.Errorf("reference request to unconfigured host %q", snapshot.Host)
		}
		body, overflow, err := captured.wait(ctx)
		if err != nil {
			return nil, err
		}
		snapshot.Body, snapshot.Overflow = body, overflow
		return errors.WithStack2(e.decoder.Request(snapshot))
	}
	route := e.route(request)
	decision, err := e.referencePoint.process(ctx, requestID, route, None[string](), payload)
	if err != nil {
		logger.FromContext(ctx).ErrorContext(ctx, "Reference request was not recorded", "error", err)
		if destination, ok := upstream.Get(); ok {
			destination.ServeHTTP(writer, outbound)
			return
		}
		writer.WriteHeader(nethttp.StatusBadGateway)
		return
	}
	e.referencePoint.respond(writer, outbound, decision, upstream, e.sourceDecode(request, route))
}

// sourceDecode decodes the reference responses that replacements read, and no others.
func (e *Egress) sourceDecode(request *nethttp.Request, route model.Route) decodeFunc {
	name, source := e.sourceResponse(route).Get()
	if !source {
		return noDecode
	}
	method, header := request.Method, request.Header.Clone()
	return func(_ context.Context, recorded *recording) engine.Content {
		response, err := recorded.response()
		if err != nil {
			return engine.Content{Failure: Some(err)}
		}
		// The decoded response outlives the recording, so it holds memory of its own.
		hold, err := recorded.hold()
		if err != nil {
			return engine.Content{Failure: Some(err)}
		}
		content := e.decoder.SourceResponse(method, header, name, response)
		content.Hold = Some[engine.Raw](hold)
		return content
	}
}

// destination returns the upstream of the first destination that matches a host,
// ignoring any port.
func (e *Egress) destination(host string) (proxy *httputil.ReverseProxy, configured bool) {
	name := hostname(host)
	for _, destination := range e.destinations {
		if destination.pattern.Match(name) {
			return destination.proxy, true
		}
	}
	return nil, false
}

// upstream returns the destination for a request's host. Other hosts are reached
// over plaintext HTTP as received, unless they would loop back to egress.
func (e *Egress) upstream(request *nethttp.Request) Option[*httputil.ReverseProxy] {
	if destination, configured := e.destination(request.Host); configured {
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
// the candidate when no reference matches, unless the proxy stopped it.
func (e *Egress) serveCandidate(writer nethttp.ResponseWriter, request *nethttp.Request) {
	ctx, cancel := context.WithCancelCause(request.Context())
	defer cancel(nil)
	run, admitted := e.candidates.Start(ctx, func() { cancel(engine.ErrCandidateStopped) }).Get()
	if !admitted {
		nethttp.Error(writer, "candidate requests are refused", nethttp.StatusServiceUnavailable)
		return
	}
	defer e.candidates.Finish(run)
	route := e.route(request)
	requestID, err := takeRequestID(request.Header)
	if err != nil {
		e.candidates.Quarantine(ctx, model.QuarantineReason{
			Kind:    model.QuarantinePairing,
			Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: script.Protocol, Route: route}),
			Detail:  err.Error(),
		})
		nethttp.Error(writer, "request ID is invalid", nethttp.StatusBadGateway)
		return
	}
	payload := func(context.Context) (schema.Value, error) {
		if _, configured := e.destination(request.Host); !configured {
			return nil, errors.Errorf("candidate request to unconfigured host %q", request.Host)
		}
		limit := e.maxBodyBytes
		body, err := io.ReadAll(io.LimitReader(request.Body, int64(limit)+1))
		if err != nil {
			return nil, errors.Wrap(err, "read candidate request body")
		}
		captured := newMessageRequest(request)
		captured.Body, captured.Overflow = body[:min(len(body), limit)], len(body) > limit
		return errors.WithStack2(e.decoder.Request(captured))
	}
	decision, err := e.candidatePoint.process(ctx, requestID, route, None[string](), payload)
	if err != nil {
		nethttp.Error(writer, "no matching reference response", nethttp.StatusBadGateway)
		return
	}
	if answer, answered := decision.(engine.DecisionAnswer); answered {
		e.answer(writer, request, route, answer.Message)
		return
	}
	e.candidatePoint.respond(writer, request, decision, None[*httputil.ReverseProxy](), noDecode)
}

// answer writes a replacement response. One that cannot be encoded quarantines the
// candidate, because the candidate would otherwise see a response the reference never could.
func (e *Egress) answer(writer nethttp.ResponseWriter, request *nethttp.Request, route model.Route, answer schema.Value) {
	encoded, err := e.decoder.Answer(request.Header, answer)
	if err != nil {
		e.candidates.Quarantine(request.Context(), model.QuarantineReason{
			Kind:    model.QuarantineUnable,
			Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: script.Protocol, Route: route}),
			Detail:  errors.Wrap(err, "encode replacement response").Error(),
		})
		nethttp.Error(writer, "replacement response cannot be encoded", nethttp.StatusBadGateway)
		return
	}
	maps.Copy(writer.Header(), encoded.Header)
	writer.Header().Set("Content-Length", strconv.Itoa(len(encoded.Body)))
	writer.WriteHeader(nethttp.StatusOK)
	if _, err := writer.Write(encoded.Body); err != nil {
		logger.FromContext(request.Context()).WarnContext(request.Context(), "Replacement response was not written", "error", err)
	}
}

// route names a request by the endpoint pattern it matches, so requests that
// differ only in path wildcards pair. Others are named by host, ignoring any port, and path.
func (e *Egress) route(request *nethttp.Request) model.Route {
	if endpoint, _, declared := e.routes.Request(request.Method, request.Host, request.URL.EscapedPath()); declared {
		return model.Route(endpoint.Pattern)
	}
	return model.Route(request.Method + " " + hostname(request.Host) + request.URL.EscapedPath())
}

// newMessageRequest describes a request without its body, detached from the
// request's own header map.
func newMessageRequest(request *nethttp.Request) message.Request {
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

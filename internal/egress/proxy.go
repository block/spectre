// Package egress forwards reference dependency requests and answers each
// candidate request by replaying an equivalent reference response.
package egress

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/middleware/health"
	"github.com/block/spectre/internal/middleware/logging"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/proxy"
)

// Proxy serves reference and candidate traffic on separate listeners. Candidate
// requests never reach a dependency under any condition.
type Proxy struct {
	config       Config
	log          *slog.Logger
	transport    http.RoundTripper
	hasher       RequestHasher
	static       *descriptorpb.FileDescriptorSet
	destinations map[string]*httputil.ReverseProxy
	listeners    []*netaddr.Endpoint
	budget       *proxy.Budget
	recorder     *recorder
	candidates   *proxy.Candidates
	requests     chan struct{}
	// hashing tracks reference hashes, which may finish after their request.
	hashing   sync.WaitGroup
	reference http.Handler
	candidate http.Handler
	health    *health.Handler
}

// RequestHasher identifies dependency requests by their normalised input.
type RequestHasher interface {
	MaxRequestBytes() int
	Configure(ctx context.Context, set *descriptorpb.FileDescriptorSet) error
	Hash(ctx context.Context, request comparison.Request) (comparison.RequestHash, error)
}

// New constructs an egress proxy from its parsed configuration.
func New(config Config, transport http.RoundTripper, hasher RequestHasher, log *slog.Logger) (*Proxy, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("transport is required")
	}
	if hasher == nil {
		return nil, errors.New("request hasher is required")
	}
	if log == nil {
		return nil, errors.New("logger is required")
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
		destination, err := proxy.NewReverseProxy(endpoint, transport, log, false)
		if err != nil {
			return nil, errors.Wrapf(err, "proxy destination %q", host)
		}
		destinations[name] = destination
	}
	static, err := descriptors.LoadDescriptorSets(config.Descriptors)
	if err != nil {
		return nil, errors.Wrap(err, "load static descriptors")
	}
	egress := &Proxy{
		config:       config,
		log:          log,
		transport:    transport,
		hasher:       hasher,
		static:       static,
		destinations: destinations,
		listeners:    listeners,
		budget:       proxy.NewBudget(config.RecordingBufferBytes),
		recorder:     newRecorder(config.MatchWindow),
		candidates:   proxy.NewCandidates(config.CandidateMaxInFlight, log),
		requests:     make(chan struct{}, config.MaxInFlightRequests),
		health:       health.New(http.NotFoundHandler()),
	}
	egress.reference = logging.New(http.HandlerFunc(egress.serveReference), log, logger.EventEgressReceived)
	egress.candidate = logging.New(http.HandlerFunc(egress.serveCandidate), log, logger.EventEgressReceived)
	return egress, nil
}

// Serve accepts reference, candidate, and health traffic until the context is
// cancelled or a server fails. Readiness remains false until the schema is configured.
func (p *Proxy) Serve(ctx context.Context, reference net.Listener, candidate net.Listener, probes net.Listener) error {
	bindings := []proxy.Binding{
		proxy.NewBinding(reference, p.reference),
		proxy.NewBinding(candidate, p.candidate),
		proxy.NewBinding(probes, p.health),
	}
	lifecycle := proxy.NewLifecycle(p.configureSchema, p.stop, p.drain)
	return errors.Wrap(proxy.Serve(ctx, p.config.Config, p.log, bindings, lifecycle), "serve egress")
}

func (p *Proxy) configureSchema(ctx context.Context) {
	if err := p.hasher.Configure(ctx, p.static); err != nil {
		p.log.ErrorContext(ctx, "Request hashing setup failed", "error", err)
		return
	}
	p.health.SetReady(true)
}

// stop cancels waiting candidates before servers shut down, because their
// handlers would otherwise hold shutdown for up to the match window.
func (p *Proxy) stop() {
	p.health.SetReady(false)
	p.candidates.Close()
}

func (p *Proxy) drain(ctx context.Context) error {
	if err := p.candidates.Shutdown(ctx); err != nil {
		return errors.WithStack(err)
	}
	hashed := make(chan struct{})
	go func() {
		p.hashing.Wait()
		close(hashed)
	}()
	select {
	case <-hashed:
		return nil
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "wait for reference hashing")
	}
}

// serveReference forwards immediately and hashes a copy of the request in
// parallel, so a failure to identify the request never affects the reference.
func (p *Proxy) serveReference(writer http.ResponseWriter, request *http.Request) {
	select {
	case p.requests <- struct{}{}:
		defer func() { <-p.requests }()
	default:
		http.Error(writer, "egress request capacity exceeded", http.StatusServiceUnavailable)
		return
	}
	ctx := request.Context()
	forward, configured := p.destinations[hostname(request.Host)]
	if !configured {
		p.candidates.Quarantine(ctx, errors.Errorf("reference request to unconfigured host %q", request.Host))
		p.forwardAsReceived(writer, request)
		return
	}
	recorded := newRecording(p.budget)
	// The transport may never run, so the handler guarantees the recording completes.
	defer recorded.finish(nil, errors.New("reference request ended without a response"))
	// Snapshot the request now, because hashing may start after the handler returns.
	identity := newHashRequest(request, nil, false, "reference")
	hashContext := context.WithoutCancel(ctx)
	captured := func(body []byte, overflow bool, complete bool) {
		identity.Body, identity.Overflow = body, overflow
		p.hashing.Go(func() { p.recordReference(hashContext, identity, complete, recorded) })
	}
	outbound := request.Clone(ctx)
	// The reverse proxy drops empty bodies without closing them, so hash those now.
	if request.Body == nil || request.Body == http.NoBody || request.ContentLength == 0 {
		captured(nil, false, true)
	} else {
		outbound.Body = newCaptureBody(request.Body, p.hasher.MaxRequestBytes(), captured)
	}
	recordingProxy(forward, recorded).ServeHTTP(writer, outbound)
}

// recordReference makes the recording available to candidates under its hash.
func (p *Proxy) recordReference(ctx context.Context, identity comparison.Request, complete bool, recorded *recording) {
	hash, err := p.identify(ctx, identity, complete)
	if err != nil {
		recorded.release()
		p.candidates.Quarantine(ctx, errors.Wrap(err, "identify reference request"))
		return
	}
	p.recorder.add(hash, recorded)
}

func (p *Proxy) identify(ctx context.Context, identity comparison.Request, complete bool) (comparison.RequestHash, error) {
	if !complete {
		return comparison.RequestHash{}, errors.New("request body was not fully read")
	}
	hash, err := p.hasher.Hash(ctx, identity)
	return hash, errors.WithStack(err)
}

// forwardAsReceived sends a request for an unconfigured host to that host over
// plaintext HTTP, refusing any host that would loop back to egress.
func (p *Proxy) forwardAsReceived(writer http.ResponseWriter, request *http.Request) {
	endpoint, err := netaddr.ParseBackend("http://" + request.Host)
	if err == nil && targetsListener(endpoint, p.listeners) {
		err = errors.New("host targets an egress listener")
	}
	var destination *httputil.ReverseProxy
	if err == nil {
		destination, err = proxy.NewReverseProxy(endpoint, p.transport, p.log, false)
	}
	if err != nil {
		p.log.ErrorContext(request.Context(), "Reference request has no usable destination", "host", request.Host, "error", err)
		writer.WriteHeader(http.StatusBadGateway)
		return
	}
	destination.ServeHTTP(writer, request)
}

// serveCandidate replays the matching reference response. A request that cannot
// be matched quarantines the candidate, unless the candidate itself gave up.
func (p *Proxy) serveCandidate(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	run, admitted := p.candidates.Start(ctx, cancel).Get()
	if !admitted {
		http.Error(writer, "candidate requests are refused", http.StatusServiceUnavailable)
		return
	}
	defer p.candidates.Finish(run)
	recorded, err := p.match(ctx, request)
	if err != nil {
		if ctx.Err() == nil {
			p.candidates.Quarantine(ctx, err)
		}
		http.Error(writer, "no matching reference response", http.StatusBadGateway)
		return
	}
	defer recorded.release()
	p.log.InfoContext(ctx, "Candidate request matched a reference request",
		"event", logger.EventCorrelation,
		"host", request.Host,
		"path", request.URL.EscapedPath(),
	)
	replayed := request.Clone(ctx)
	replayed.Body = http.NoBody
	replayed.ContentLength = 0
	newReplayProxy(recorded, p.log).ServeHTTP(writer, replayed)
}

// match claims the reference recording for an equivalent request and waits for
// its response to finish.
func (p *Proxy) match(ctx context.Context, request *http.Request) (*recording, error) {
	if _, configured := p.destinations[hostname(request.Host)]; !configured {
		return nil, errors.Errorf("candidate request to unconfigured host %q", request.Host)
	}
	limit := p.hasher.MaxRequestBytes()
	body, err := io.ReadAll(io.LimitReader(request.Body, int64(limit)+1))
	if err != nil {
		return nil, errors.Wrap(err, "read candidate request body")
	}
	overflow := len(body) > limit
	hash, err := p.hasher.Hash(ctx, newHashRequest(request, body[:min(len(body), limit)], overflow, "candidate"))
	if err != nil {
		return nil, errors.Wrap(err, "identify candidate request")
	}
	recorded, err := p.recorder.claim(ctx, hash)
	if err != nil {
		return nil, err
	}
	if err := recorded.wait(ctx); err != nil {
		recorded.release()
		return nil, err
	}
	return recorded, nil
}

// newHashRequest describes a request for hashing, detached from the request's
// own header map.
func newHashRequest(request *http.Request, body []byte, overflow bool, side string) comparison.Request {
	return comparison.Request{
		Method:   request.Method,
		Host:     request.Host,
		Path:     request.URL.EscapedPath(),
		RawQuery: request.URL.RawQuery,
		Header:   request.Header.Clone(),
		Body:     body,
		Overflow: overflow,
		Side:     side,
	}
}

// recordingProxy copies a destination's reverse proxy so that one request's
// response is recorded. The copy shares the destination's forwarding setup.
func recordingProxy(destination *httputil.ReverseProxy, recorded *recording) *httputil.ReverseProxy {
	forward := *destination
	forward.Transport = newRecordingTransport(destination.Transport, recorded)
	return &forward
}

// newReplayProxy answers one candidate request from its recording, reusing the
// reverse proxy's header and trailer handling.
func newReplayProxy(recorded *recording, log *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		// The recording answers without dialing, so the outbound URL is never used.
		Rewrite:   func(*httputil.ProxyRequest) {},
		Transport: recorded,
		ErrorLog:  slog.NewLogLogger(log.Handler(), slog.LevelError),
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
			log.WarnContext(request.Context(), "Candidate replay failed", "error", err)
			writer.WriteHeader(http.StatusBadGateway)
		},
	}
}

// hostname selects a destination by host name alone, ignoring any port.
func hostname(host string) string {
	return strings.ToLower((&url.URL{Host: host}).Hostname())
}

func targetsListener(endpoint *netaddr.Endpoint, listeners []*netaddr.Endpoint) bool {
	return slices.ContainsFunc(listeners, endpoint.TargetsListener)
}

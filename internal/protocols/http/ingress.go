package http

import (
	"context"
	"io"
	"maps"
	"net"
	nethttp "net/http"
	"net/http/httputil"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison"
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
	"github.com/block/spectre/internal/state"
)

// Ingress mirrors each inbound request to the reference and candidate services.
// Reference responses never wait for candidate comparison.
type Ingress struct {
	referenceProxy *httputil.ReverseProxy
	candidateProxy *httputil.ReverseProxy
	// referenceBackend and candidateBackend are dialed until each first accepts.
	referenceBackend *netaddr.Endpoint
	candidateBackend *netaddr.Endpoint
	// serving admits requests once startup completes. Shutdown never clears it,
	// so requests that arrive while servers drain still reach the reference.
	serving   atomic.Bool
	reference *point
	candidate *point
	config    IngressConfig
	scripts   *comparison.Scripts
	handle    script.Handle
	routes    *route.Matcher
	buffer    *proxy.Budget
	requests  chan struct{}
	// health is the shared readiness, and requestHandler serves its endpoints in front of ingress.
	health         *health.Handler
	requestHandler nethttp.Handler
	descriptors    Option[DescriptorLoader]
	static         *descriptorpb.FileDescriptorSet
	candidates     *proxy.Candidates
	// quarantiner is the only way ingress quarantines, so every reason names its route's node.
	quarantiner engine.Quarantiner
	// migration maps each route in the migration state to its node.
	migration Option[*route.Map[stateRoute]]
	// mu guards decoder, which is set once descriptors are configured.
	mu      sync.RWMutex
	decoder Option[*message.Decoder]
}

// DescriptorLoader loads a backend's protobuf descriptor set.
type DescriptorLoader interface {
	Load(ctx context.Context, endpoint string) (*descriptorpb.FileDescriptorSet, error)
}

// NewIngress builds ingress on the shared side that the mirror closes and shuts down.
// The side's quarantiner names a route's node, and loader is required only for reflection.
func NewIngress(
	ctx context.Context,
	config IngressConfig,
	handle script.Handle,
	runtime protocols.Runtime,
	side protocols.Side,
	transport nethttp.RoundTripper,
	loader Option[DescriptorLoader],
) (*Ingress, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("transport is required")
	}
	if runtime.Scripts == nil || side.Candidates == nil || side.Quarantiner == nil || side.Health == nil {
		return nil, errors.New("scripts, candidates, quarantiner and health are required")
	}
	if config.Reflection && !loader.Ok() {
		return nil, errors.New("descriptor loader is required")
	}
	reference, candidate, err := ingressBackends(config)
	if err != nil {
		return nil, err
	}
	static, err := descriptors.LoadDescriptorSets(config.Descriptors)
	if err != nil {
		return nil, errors.Wrap(err, "load static descriptors")
	}
	referenceProxy, err := NewReverseProxy(ctx, reference, transport, false)
	if err != nil {
		return nil, errors.Wrap(err, "proxy reference backend")
	}
	candidateProxy, err := NewReverseProxy(ctx, candidate, transport, true)
	if err != nil {
		return nil, errors.Wrap(err, "proxy candidate backend")
	}
	migrationRoutes, err := stateRoutes(side.Nodes)
	if err != nil {
		return nil, errors.Wrap(err, "route migration state")
	}
	routes, err := route.NewMatcher(runtime.Scripts.Endpoints(), handle.Declared(runtime.Scripts.Program()).Rules)
	if err != nil {
		return nil, errors.Wrap(err, "configure response comparison")
	}
	// Each response is decoded and dropped, so its recording needs only its own limit.
	responseBudget := func() *proxy.Budget { return proxy.NewBudget(config.ComparisonMaxBodyBytes) }
	ingress := &Ingress{
		referenceProxy:   referenceProxy,
		candidateProxy:   candidateProxy,
		referenceBackend: reference,
		candidateBackend: candidate,
		reference:        newPoint(side.Reference, responseBudget, true, false),
		candidate:        newPoint(side.Candidate, responseBudget, false, false),
		config:           config,
		scripts:          runtime.Scripts,
		handle:           handle,
		routes:           routes,
		buffer:           proxy.NewBudget(config.CandidateBufferBytes),
		requests:         make(chan struct{}, config.MaxInFlightRequests),
		descriptors:      loader,
		static:           static,
		candidates:       side.Candidates,
		quarantiner:      side.Quarantiner,
		health:           side.Health,
		migration:        migrationRoutes,
	}
	requestHandler := logging.New(nethttp.HandlerFunc(ingress.serveProxy), logger.EventReceived)
	ingress.requestHandler = side.Health.Wrap(requestHandler)
	return ingress, nil
}

// stateRoute is a migration state route pattern and the node that lists it.
type stateRoute struct {
	pattern  model.Route
	nodeID   string
	migrated bool
}

// stateRoutes maps the ingress[http] routes of each node to the node.
func stateRoutes(migration Option[[]state.KindNode]) (Option[*route.Map[stateRoute]], error) {
	nodes, ok := migration.Get()
	if !ok {
		return None[*route.Map[stateRoute]](), nil
	}
	routes := route.New[stateRoute]()
	for _, node := range nodes {
		for _, pattern := range slices.Sorted(maps.Keys(node.Settings["routes"])) {
			listed := stateRoute{pattern: model.Route(pattern), nodeID: node.ID, migrated: node.Migrated}
			if err := routes.Add(pattern, listed); err != nil {
				return None[*route.Map[stateRoute]](), errors.Wrapf(err, "node %s", node.ID)
			}
		}
	}
	return Some(routes), nil
}

// route is a request's engine route: the migration state pattern that lists it,
// whose host lets its node be found, or otherwise its method and escaped path.
func (i *Ingress) route(request *nethttp.Request) model.Route {
	path := request.URL.EscapedPath()
	if routes, ok := i.migration.Get(); ok {
		if listed, _, matched := routes.Match(request.Method, request.Host, path); matched {
			return listed.pattern
		}
	}
	return model.Route(request.Method + " " + path)
}

func (i *Ingress) reason(kind model.QuarantineKind, request *nethttp.Request, detail string) model.QuarantineReason {
	return model.QuarantineReason{
		Kind:    kind,
		Traffic: Some(model.Traffic{Direction: model.Ingress, Protocol: script.Protocol, Route: i.route(request)}),
		Detail:  detail,
	}
}

func ingressBackends(config IngressConfig) (reference, candidate *netaddr.Endpoint, err error) {
	reference, err = netaddr.ParseBackend(config.Reference)
	if err != nil {
		return nil, nil, errors.Wrap(err, "parse reference backend")
	}
	candidate, err = netaddr.ParseBackend(config.Candidate)
	if err != nil {
		return nil, nil, errors.Wrap(err, "parse candidate backend")
	}
	listen := netaddr.ParseListen(config.Listen)
	switch {
	case !candidate.IsUnix() && !candidate.IsLoopback():
		return nil, nil, errors.New("candidate backend must use a loopback IP address or a unix socket")
	case reference.SameDestination(candidate):
		return nil, nil, errors.New("reference and candidate backends must be different")
	case candidate.TargetsListener(listen):
		return nil, nil, errors.New("candidate backend must not target the ingress listener")
	case reference.TargetsListener(listen):
		return nil, nil, errors.New("reference backend must not target the ingress listener")
	}
	return reference, candidate, nil
}

// ServeHTTP serves health checks or mirrors a request to both backends.
func (i *Ingress) ServeHTTP(writer nethttp.ResponseWriter, request *nethttp.Request) {
	i.requestHandler.ServeHTTP(writer, request)
}

// Health reports the ingress readiness.
func (i *Ingress) Health() *health.Handler {
	return i.health
}

func (i *Ingress) serveProxy(writer nethttp.ResponseWriter, request *nethttp.Request) {
	if !i.serving.Load() {
		nethttp.Error(writer, "ingress is not ready", nethttp.StatusServiceUnavailable)
		return
	}
	route := i.route(request)
	select {
	case i.requests <- struct{}{}:
		defer func() { <-i.requests }()
	default:
		nethttp.Error(writer, "ingress request capacity exceeded", nethttp.StatusServiceUnavailable)
		return
	}
	referenceRequest := request.Clone(request.Context())
	referenceRequest.GetBody = nil
	if !i.mirrors(request) {
		i.referenceProxy.ServeHTTP(writer, referenceRequest)
		return
	}
	// Candidate work may outlive reference delivery, but its own timeout bounds its
	// lifetime, and a cancelled reference ends it.
	candidateContext := context.WithoutCancel(request.Context())
	candidateContext, cancel := context.WithTimeout(candidateContext, i.config.CandidateTimeout)
	run, admitted := i.candidates.Start(candidateContext, cancel).Get()
	if !admitted {
		cancel()
		i.referenceProxy.ServeHTTP(writer, referenceRequest)
		return
	}
	ignored, unsupported, decode := i.classify(request)
	// Only admitted requests quarantine, so a stopping or quarantined candidate never does.
	if reason, ok := unsupported.Get(); ok {
		cancel()
		i.candidates.Finish(run)
		i.quarantiner.Quarantine(request.Context(), i.reason(model.QuarantineUnable, request, reason))
		i.referenceProxy.ServeHTTP(writer, referenceRequest)
		return
	}
	reference, err := i.reference.process(request.Context(), None[engine.RequestID](), route, ignored, unreadRequest)
	forward, forwarded := reference.(engine.DecisionForward)
	if err != nil || !forwarded {
		logger.FromContext(request.Context()).ErrorContext(request.Context(), "Reference request was not paired", "error", err)
		cancel()
		i.candidates.Finish(run)
		i.referenceProxy.ServeHTTP(writer, referenceRequest)
		return
	}
	// The candidate joins its pair before the reference is sent, as the engine requires.
	candidate, err := i.candidate.process(candidateContext, forward.RequestID, route, ignored, unreadRequest)
	if err != nil {
		cancel()
		i.candidates.Finish(run)
	} else {
		body := i.mirror(candidateContext, request, referenceRequest, cancel)
		candidateRequest := request.Clone(candidateContext)
		candidateRequest.Body = body
		candidateRequest.GetBody = nil
		go func() {
			defer cancel()
			defer i.candidates.Finish(run)
			defer body.closeReader()
			i.serveCandidate(candidateRequest, candidate, decode)
		}()
	}
	// A cancelled reference has no response to compare, so the candidate's run is
	// discarded too. An aborted response panics, and the engine counts it as cancelled.
	referenceCancelled := true
	defer func() {
		if referenceCancelled {
			cancel()
		}
	}()
	referenceCancelled = i.reference.respond(writer, referenceRequest, reference, Some(i.referenceProxy), decode)
}

// mirrors reports whether the migration state lets a request reach the candidate.
// A route no node lists quarantines the candidate, unless ingress never compares it.
func (i *Ingress) mirrors(request *nethttp.Request) bool {
	routes, ok := i.migration.Get()
	if !ok {
		return true
	}
	path := request.URL.EscapedPath()
	owner, _, listed := routes.Match(request.Method, request.Host, path)
	if listed {
		return owner.migrated
	}
	if _, ignored := i.routes.Responses(request.Method, path, request.Header); ignored.Ok() || strings.HasPrefix(path, "/grpc.reflection.") {
		return false
	}
	i.quarantiner.Quarantine(request.Context(), i.reason(model.QuarantineUnlisted, request,
		"route "+request.Method+" "+path+" is in no node of the migration state"))
	return false
}

// mirror makes the reference request body the sole producer of the candidate's body.
func (i *Ingress) mirror(ctx context.Context, request, referenceRequest *nethttp.Request, abort context.CancelFunc) *streamBody {
	body := newStreamBody(i.buffer, func(err error) {
		i.quarantiner.Quarantine(ctx, i.reason(model.QuarantineCapacity, request, err.Error()))
	}, abort)
	if request.Body == nil || request.Body == nethttp.NoBody {
		body.closeWriter(io.EOF)
		return body
	}
	referenceRequest.Body = newTeeBody(request.Body, body)
	return body
}

// classify decides at request time whether, and how, both responses are compared.
// An unsupported request cannot be compared at all.
func (i *Ingress) classify(request *nethttp.Request) (ignored, unsupported Option[string], decode decodeFunc) {
	i.mu.RLock()
	decoder, ready := i.decoder.Get()
	i.mu.RUnlock()
	if !ready {
		return Some("comparison schema is not ready"), None[string](), noDecode
	}
	plan := decoder.Responses(request.Method, request.URL.EscapedPath(), request.URL.RawQuery, request.Header)
	return plan.Ignored(), plan.Unsupported(), func(ctx context.Context, recorded *recording) engine.Content {
		response, err := recorded.response()
		if err != nil {
			return engine.Content{Failure: Some(err)}
		}
		return plan.Decode(ctx, response)
	}
}

// serveCandidate proxies on a goroutine the HTTP server does not supervise, so it
// recovers the panic ReverseProxy raises to abort a response it cannot finish copying.
func (i *Ingress) serveCandidate(request *nethttp.Request, decision engine.Decision, decode decodeFunc) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if err, ok := recovered.(error); !ok || !errors.Is(err, nethttp.ErrAbortHandler) {
			panic(recovered)
		}
	}()
	i.candidate.respond(newDiscardResponseWriter(), request, decision, Some(i.candidateProxy), decode)
}

// Serve accepts ingress traffic until the context is cancelled or the server fails.
// It rejects requests until both backends accept and the schema is configured.
func (i *Ingress) Serve(ctx context.Context, listener net.Listener) error {
	bindings := []httpservice.Binding{httpservice.NewBinding(listener, i)}
	lifecycle := httpservice.NewLifecycle(i.start, func() {}, func(context.Context) error { return nil })
	return errors.Wrap(httpservice.Serve(ctx, i.config.ServerConfig.ServeConfig(), bindings, lifecycle), "serve ingress")
}

// start waits for the backends in the background, so Serve can still stop.
func (i *Ingress) start(ctx context.Context) {
	go i.awaitBackends(ctx)
}

// awaitBackends serves and readies ingress once both backends accept and share a
// schema. A candidate that fails startup is quarantined, and ingress stays unready.
func (i *Ingress) awaitBackends(ctx context.Context) {
	if err := i.referenceBackend.AwaitAccepting(ctx); err != nil {
		return
	}
	logger.FromContext(ctx).InfoContext(ctx, "Reference accepts connections")
	if !i.awaitCandidate(ctx) {
		return
	}
	err := i.configureSchema(ctx)
	switch {
	case ctx.Err() != nil:
		return
	case err != nil:
		i.quarantiner.Quarantine(ctx, model.QuarantineReason{Kind: model.QuarantineUnable, Detail: err.Error()})
		return
	}
	i.serving.Store(true)
	i.health.SetReady(true)
	logger.FromContext(ctx).InfoContext(ctx, "Ingress is ready; mirroring starts")
}

// awaitCandidate reports whether the candidate accepts a connection, and
// quarantines it when it does not within the startup timeout.
func (i *Ingress) awaitCandidate(ctx context.Context) (started bool) {
	startup, cancel := context.WithTimeout(ctx, i.config.CandidateStartupTimeout)
	defer cancel()
	err := i.candidateBackend.AwaitAccepting(startup)
	switch {
	case ctx.Err() != nil:
		return false
	case err != nil:
		i.quarantiner.Quarantine(ctx, model.QuarantineReason{
			Kind:   model.QuarantineUnable,
			Detail: "candidate did not accept a connection within " + i.config.CandidateStartupTimeout.String(),
		})
		return false
	}
	logger.FromContext(ctx).InfoContext(ctx, "Candidate accepts connections")
	return true
}

// configureSchema builds the decoder from one schema shared by both backends.
// Static descriptors are identical for both, so only reflected ones are compared.
func (i *Ingress) configureSchema(ctx context.Context) error {
	reflectionContext, cancel := context.WithTimeout(ctx, i.config.ReflectionTimeout)
	defer cancel()
	set := i.static
	if i.config.Reflection {
		reference, candidate, err := i.loadDescriptors(reflectionContext)
		if err != nil {
			return err
		}
		if !proto.Equal(reference, candidate) {
			return errors.New("reference and candidate descriptors differ")
		}
		set, err = descriptors.Merge(reference, i.static)
		if err != nil {
			return errors.Wrap(err, "merge backend and static descriptors")
		}
	}
	decoder, err := message.New(i.scripts, i.handle, set, i.routes, i.config.ComparisonMaxBodyBytes)
	if err != nil {
		return errors.Wrap(err, "configure response decoding")
	}
	i.mu.Lock()
	i.decoder = Some(decoder)
	i.mu.Unlock()
	return nil
}

func (i *Ingress) loadDescriptors(ctx context.Context) (*descriptorpb.FileDescriptorSet, *descriptorpb.FileDescriptorSet, error) {
	type descriptorResult struct {
		name string
		set  *descriptorpb.FileDescriptorSet
		err  error
	}
	loader, ok := i.descriptors.Get()
	if !ok {
		return nil, nil, errors.New("descriptor loader is required")
	}
	results := make(chan descriptorResult, 2)
	// Both loads share one deadline and each goroutine publishes exactly one result.
	for name, endpoint := range map[string]string{"reference": i.config.Reference, "candidate": i.config.Candidate} {
		go func() {
			set, err := loader.Load(ctx, endpoint)
			results <- descriptorResult{name: name, set: set, err: err}
		}()
	}
	sets := map[string]*descriptorpb.FileDescriptorSet{}
	for range 2 {
		loaded := <-results
		if loaded.err != nil {
			return nil, nil, errors.Wrapf(loaded.err, "load %s descriptors", loaded.name)
		}
		if loaded.set == nil {
			return nil, nil, errors.Errorf("load %s descriptors: descriptor set is nil", loaded.name)
		}
		sets[loaded.name] = loaded.set
	}
	return sets["reference"], sets["candidate"], nil
}

// discardResponseWriter provides the candidate proxy sink beneath recording.
type discardResponseWriter struct {
	header nethttp.Header
}

func newDiscardResponseWriter() *discardResponseWriter {
	return &discardResponseWriter{header: make(nethttp.Header)}
}

func (w *discardResponseWriter) Header() nethttp.Header {
	return w.header
}

func (w *discardResponseWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

func (w *discardResponseWriter) WriteHeader(int) {}

func (w *discardResponseWriter) Flush() {}

package httpserver

import (
	"context"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"slices"
	"strings"
	"sync"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

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
	"github.com/block/spectre/internal/state"
)

// Ingress mirrors each inbound request to the reference and candidate services.
// Reference responses never wait for candidate comparison.
type Ingress struct {
	referenceProxy *httputil.ReverseProxy
	candidateProxy *httputil.ReverseProxy
	reference      *point
	candidate      *point
	config         IngressConfig
	engine         *protocol.Engine
	routes         *route.Matcher
	buffer         *proxy.Budget
	requests       chan struct{}
	health         *health.Handler
	descriptors    Option[DescriptorLoader]
	static         *descriptorpb.FileDescriptorSet
	candidates     *proxy.Candidates
	// migration maps each route in the migration state to its node's status.
	migration Option[*route.Map[state.Status]]
	// mu guards decoder, which is set once descriptors are configured.
	mu      sync.RWMutex
	decoder Option[*message.Decoder]
}

// DescriptorLoader loads a backend's protobuf descriptor set.
type DescriptorLoader interface {
	Load(ctx context.Context, endpoint string) (*descriptorpb.FileDescriptorSet, error)
}

// NewIngress constructs an ingress proxy and the engine it owns. loader is required
// only for reflection; with a migration state, only migrated routes are mirrored.
func NewIngress(
	ctx context.Context,
	config IngressConfig,
	engineConfig protocol.Config,
	transport http.RoundTripper,
	loader Option[DescriptorLoader],
	migration Option[state.State],
) (*Ingress, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("transport is required")
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
	migrationRoutes, err := stateRoutes(migration)
	if err != nil {
		return nil, errors.Wrap(err, "route migration state")
	}
	candidates := proxy.NewCandidates(config.Candidates)
	routes := route.NewMatcher()
	engine, err := protocol.NewEngine(ctx, engineConfig, map[boundary.Protocol]protocol.Matcher{boundary.ProtocolHTTP: routes}, candidates)
	if err != nil {
		return nil, errors.Wrap(err, "configure response comparison")
	}
	// Each response is decoded and dropped, so its recording needs only its own limit.
	responseBudget := func() *proxy.Budget { return proxy.NewBudget(config.ComparisonMaxBodyBytes) }
	ingress := &Ingress{
		referenceProxy: referenceProxy,
		candidateProxy: candidateProxy,
		reference:      newPoint(engine.IngressReference(boundary.ProtocolHTTP), responseBudget, true, false),
		candidate:      newPoint(engine.IngressCandidate(boundary.ProtocolHTTP), responseBudget, false, false),
		config:         config,
		engine:         engine,
		routes:         routes,
		buffer:         proxy.NewBudget(config.CandidateBufferBytes),
		requests:       make(chan struct{}, config.MaxInFlightRequests),
		descriptors:    loader,
		static:         static,
		candidates:     candidates,
		migration:      migrationRoutes,
	}
	requestHandler := logging.New(http.HandlerFunc(ingress.serveProxy), logger.EventReceived)
	ingress.health = health.New(requestHandler)
	return ingress, nil
}

// stateRoutes maps the ingress[http] routes of each node to the node's status.
func stateRoutes(migration Option[state.State]) (Option[*route.Map[state.Status]], error) {
	nodes, ok := migration.Get()
	if !ok {
		return None[*route.Map[state.Status]](), nil
	}
	kind := boundary.Kind{Direction: boundary.Ingress, Protocol: boundary.ProtocolHTTP}
	routes := route.New[state.Status]()
	for _, node := range nodes.Nodes {
		entries, _ := node.IO[kind][routesSetting].(map[string]any)
		for _, pattern := range slices.Sorted(maps.Keys(entries)) {
			if err := routes.Add(pattern, node.Status); err != nil {
				return None[*route.Map[state.Status]](), errors.Wrapf(err, "node %s", node.ID)
			}
		}
	}
	return Some(routes), nil
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
func (i *Ingress) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	i.health.ServeHTTP(writer, request)
}

// Candidates reports the ingress candidate quarantine state.
func (i *Ingress) Candidates() *proxy.Candidates {
	return i.candidates
}

func (i *Ingress) serveProxy(writer http.ResponseWriter, request *http.Request) {
	select {
	case i.requests <- struct{}{}:
		defer func() { <-i.requests }()
	default:
		http.Error(writer, "ingress request capacity exceeded", http.StatusServiceUnavailable)
		return
	}
	referenceRequest := request.Clone(request.Context())
	referenceRequest.GetBody = nil
	if !i.mirrors(request) {
		i.referenceProxy.ServeHTTP(writer, referenceRequest)
		return
	}
	// Candidate work may outlive reference delivery, but its own timeout bounds its lifetime.
	candidateContext := context.WithoutCancel(request.Context())
	candidateContext, cancel := context.WithTimeout(candidateContext, i.config.CandidateTimeout)
	run, admitted := i.candidates.Start(candidateContext, cancel).Get()
	if !admitted {
		cancel()
		i.referenceProxy.ServeHTTP(writer, referenceRequest)
		return
	}
	route := protocol.Route(request.Method + " " + request.URL.EscapedPath())
	ignored, decode := i.classify(request)
	reference, err := i.reference.process(request.Context(), None[protocol.RequestID](), route, ignored, unreadRequest)
	forward, forwarded := reference.(protocol.DecisionForward)
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
	i.reference.respond(writer, referenceRequest, reference, Some(i.referenceProxy), decode)
}

// mirrors reports whether the migration state lets a request reach the candidate.
// A route no node lists quarantines the candidate, unless ingress never compares it.
func (i *Ingress) mirrors(request *http.Request) bool {
	routes, ok := i.migration.Get()
	if !ok {
		return true
	}
	path := request.URL.EscapedPath()
	status, _, listed := routes.Match(request.Method, request.Host, path)
	if listed {
		return status == state.Migrated
	}
	if _, ignored, matched := i.routes.Response(request.Method, path); (matched && ignored.Ok()) || strings.HasPrefix(path, "/grpc.reflection.") {
		return false
	}
	i.candidates.Quarantine(request.Context(), errors.Errorf("route %s %s is in no node of the migration state", request.Method, path))
	return false
}

// mirror makes the reference request body the sole producer of the candidate's body.
func (i *Ingress) mirror(ctx context.Context, request, referenceRequest *http.Request, abort context.CancelFunc) *streamBody {
	body := newStreamBody(i.buffer, func(err error) {
		i.candidates.Quarantine(ctx, err)
	}, abort)
	if request.Body == nil || request.Body == http.NoBody {
		body.closeWriter(io.EOF)
		return body
	}
	referenceRequest.Body = newTeeBody(request.Body, body)
	return body
}

// classify decides at request time whether, and how, both responses are compared.
func (i *Ingress) classify(request *http.Request) (ignored Option[string], decode decodeFunc) {
	i.mu.RLock()
	decoder, ready := i.decoder.Get()
	i.mu.RUnlock()
	if !ready {
		return Some("comparison schema is not ready"), noDecode
	}
	plan := decoder.Responses(request.Method, request.URL.EscapedPath(), request.URL.RawQuery, request.Header)
	return plan.Ignored(), func(recorded *recording) protocol.Content {
		response, err := recorded.response()
		if err != nil {
			return protocol.Content{Failure: Some(err)}
		}
		return plan.Decode(response)
	}
}

// serveCandidate proxies on a goroutine the HTTP server does not supervise, so it
// recovers the panic ReverseProxy raises to abort a response it cannot finish copying.
func (i *Ingress) serveCandidate(request *http.Request, decision protocol.Decision, decode decodeFunc) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if err, ok := recovered.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
			panic(recovered)
		}
	}()
	i.candidate.respond(newDiscardResponseWriter(), request, decision, Some(i.candidateProxy), decode)
}

// Shutdown stops accepting candidate work and waits for active mirrors to finish.
// Ingress owns its engine, so it also shuts the engine down.
func (i *Ingress) Shutdown(ctx context.Context) error {
	if err := i.candidates.Shutdown(ctx); err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(i.engine.Shutdown(ctx))
}

// Serve accepts ingress traffic until the context is cancelled or the server fails.
// Readiness remains false until the comparison schema is configured.
func (i *Ingress) Serve(ctx context.Context, listener net.Listener) error {
	bindings := []Binding{NewBinding(listener, i)}
	lifecycle := NewLifecycle(i.configureSchema, i.markUnready, i.Shutdown)
	return errors.Wrap(Serve(ctx, i.config.Config, bindings, lifecycle), "serve ingress")
}

func (i *Ingress) markUnready() {
	i.health.SetReady(false)
}

// configureSchema makes readiness contingent on one schema shared by both backends.
// Static descriptors are identical for both, so only reflected ones are compared.
func (i *Ingress) configureSchema(ctx context.Context) {
	reflectionContext, cancel := context.WithTimeout(ctx, i.config.ReflectionTimeout)
	defer cancel()
	set := i.static
	if i.config.Reflection {
		reference, candidate, err := i.loadDescriptors(reflectionContext)
		if err != nil {
			logger.FromContext(ctx).ErrorContext(ctx, "Backend descriptor loading failed", "error", err)
			return
		}
		if !proto.Equal(reference, candidate) {
			logger.FromContext(ctx).ErrorContext(ctx, "Backend descriptors differ")
			return
		}
		set, err = descriptors.Merge(reference, i.static)
		if err != nil {
			logger.FromContext(ctx).ErrorContext(ctx, "Backend descriptors conflict with static descriptors", "error", err)
			return
		}
	}
	decoder, err := message.New(i.engine.Schema(), set, i.routes, i.config.ComparisonMaxBodyBytes)
	if err != nil {
		logger.FromContext(ctx).ErrorContext(ctx, "Response comparison setup failed", "error", err)
		return
	}
	i.mu.Lock()
	i.decoder = Some(decoder)
	i.mu.Unlock()
	i.health.SetReady(true)
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
	header http.Header
}

func newDiscardResponseWriter() *discardResponseWriter {
	return &discardResponseWriter{header: make(http.Header)}
}

func (w *discardResponseWriter) Header() http.Header {
	return w.header
}

func (w *discardResponseWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

func (w *discardResponseWriter) WriteHeader(int) {}

func (w *discardResponseWriter) Flush() {}

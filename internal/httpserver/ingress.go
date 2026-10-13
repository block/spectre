package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison/javascript"
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
	log            *slog.Logger
	buffer         *proxy.Budget
	requests       chan struct{}
	health         *health.Handler
	descriptors    Option[DescriptorLoader]
	static         *descriptorpb.FileDescriptorSet
	candidates     *proxy.Candidates
	// mu guards decoder, which is set once descriptors are configured.
	mu      sync.RWMutex
	decoder Option[*message.Decoder]
}

// DescriptorLoader loads a backend's protobuf descriptor set.
type DescriptorLoader interface {
	Load(ctx context.Context, endpoint string) (*descriptorpb.FileDescriptorSet, error)
}

// NewIngress constructs an ingress proxy and the engine it owns. loader is
// required only for reflection.
func NewIngress(
	ctx context.Context,
	config IngressConfig,
	engineConfig protocol.Config,
	transport http.RoundTripper,
	loader Option[DescriptorLoader],
	log *slog.Logger,
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
	if log == nil {
		return nil, errors.New("logger is required")
	}
	reference, candidate, err := ingressBackends(config)
	if err != nil {
		return nil, err
	}
	static, err := descriptors.LoadDescriptorSets(config.Descriptors)
	if err != nil {
		return nil, errors.Wrap(err, "load static descriptors")
	}
	referenceProxy, err := NewReverseProxy(reference, transport, log, false)
	if err != nil {
		return nil, errors.Wrap(err, "proxy reference backend")
	}
	candidateProxy, err := NewReverseProxy(candidate, transport, log, true)
	if err != nil {
		return nil, errors.Wrap(err, "proxy candidate backend")
	}
	candidates := proxy.NewCandidates(config.Candidates, log)
	routes := route.NewMatcher()
	engine, err := protocol.NewEngine(ctx, engineConfig, map[javascript.Protocol]protocol.Matcher{javascript.ProtocolHTTP: routes}, candidates, log)
	if err != nil {
		return nil, errors.Wrap(err, "configure response comparison")
	}
	// Each response is decoded and dropped, so its recording needs only its own limit.
	responseBudget := func() *proxy.Budget { return proxy.NewBudget(config.ComparisonMaxBodyBytes) }
	ingress := &Ingress{
		referenceProxy: referenceProxy,
		candidateProxy: candidateProxy,
		reference:      newPoint(engine.IngressReference(javascript.ProtocolHTTP), log, responseBudget, true, false),
		candidate:      newPoint(engine.IngressCandidate(javascript.ProtocolHTTP), log, responseBudget, false, false),
		config:         config,
		engine:         engine,
		routes:         routes,
		log:            log,
		buffer:         proxy.NewBudget(config.CandidateBufferBytes),
		requests:       make(chan struct{}, config.MaxInFlightRequests),
		descriptors:    loader,
		static:         static,
		candidates:     candidates,
	}
	requestHandler := logging.New(http.HandlerFunc(ingress.serveProxy), log, logger.EventReceived)
	ingress.health = health.New(requestHandler)
	return ingress, nil
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

func (i *Ingress) serveProxy(writer http.ResponseWriter, request *http.Request) {
	select {
	case i.requests <- struct{}{}:
		defer func() { <-i.requests }()
	default:
		http.Error(writer, "ingress request capacity exceeded", http.StatusServiceUnavailable)
		return
	}
	// Candidate work may outlive reference delivery, but its own timeout bounds its lifetime.
	candidateContext := context.WithoutCancel(request.Context())
	candidateContext, cancel := context.WithTimeout(candidateContext, i.config.CandidateTimeout)
	referenceRequest := request.Clone(request.Context())
	referenceRequest.GetBody = nil
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
		i.log.ErrorContext(request.Context(), "Reference request was not paired", "error", err)
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
	return errors.Wrap(Serve(ctx, i.config.Config, i.log, bindings, lifecycle), "serve ingress")
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
			i.log.ErrorContext(ctx, "Backend descriptor loading failed", "error", err)
			return
		}
		if !proto.Equal(reference, candidate) {
			i.log.ErrorContext(ctx, "Backend descriptors differ")
			return
		}
		set, err = descriptors.Merge(reference, i.static)
		if err != nil {
			i.log.ErrorContext(ctx, "Backend descriptors conflict with static descriptors", "error", err)
			return
		}
	}
	decoder, err := message.New(i.engine.Schema(), set, i.routes, i.config.ComparisonMaxBodyBytes)
	if err != nil {
		i.log.ErrorContext(ctx, "Response comparison setup failed", "error", err)
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

// Package ingress mirrors inbound HTTP requests to reference and candidate services.
package ingress

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/middleware/health"
	"github.com/block/spectre/internal/middleware/logging"
)

// Handler returns reference responses without waiting for candidate comparison.
// It tracks every candidate run so quarantine and shutdown can cancel all owned work.
type Handler struct {
	reference   *httputil.ReverseProxy
	candidate   *httputil.ReverseProxy
	config      Config
	log         *slog.Logger
	buffer      *bufferBudget
	requests    chan struct{}
	health      *health.Handler
	descriptors DescriptorLoader
	comparator  ResponseComparator

	// mu serializes candidate admission with process-lifetime quarantine and shutdown.
	mu            sync.Mutex
	closing       bool
	quarantined   bool
	candidateRuns map[*candidateRun]struct{}
	idle          chan struct{}
}

// DescriptorLoader loads a backend's protobuf descriptor set.
type DescriptorLoader interface {
	Load(ctx context.Context, endpoint string) (*descriptorpb.FileDescriptorSet, error)
}

// ResponseComparator compares paired backend responses against a shared schema.
type ResponseComparator interface {
	MaxResponseBytes() int
	Configure(ctx context.Context, set *descriptorpb.FileDescriptorSet) error
	Compare(
		ctx context.Context,
		requestPath string,
		requestContentType string,
		reference comparison.Response,
		candidate comparison.Response,
	) comparison.Result
}

// candidateRun gives each non-comparable cancellation function stable map identity.
type candidateRun struct {
	cancelContext context.CancelFunc
}

func newCandidateRun(cancel context.CancelFunc) *candidateRun {
	return &candidateRun{cancelContext: cancel}
}

func (r *candidateRun) cancel() {
	r.cancelContext()
}

// New constructs an ingress handler from its parsed configuration.
func New(
	config Config,
	transport http.RoundTripper,
	descriptors DescriptorLoader,
	comparator ResponseComparator,
	log *slog.Logger,
) (*Handler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("transport is required")
	}
	if descriptors == nil {
		return nil, errors.New("descriptor loader is required")
	}
	if comparator == nil {
		return nil, errors.New("response comparator is required")
	}
	if log == nil {
		return nil, errors.New("logger is required")
	}
	reference, err := parseBackendURL(config.Reference)
	if err != nil {
		return nil, errors.Wrap(err, "parse reference backend")
	}
	candidate, err := parseBackendURL(config.Candidate)
	if err != nil {
		return nil, errors.Wrap(err, "parse candidate backend")
	}
	if !candidate.isLoopback() {
		return nil, errors.New("candidate backend must use a literal loopback IP address")
	}
	if reference.identity() == candidate.identity() {
		return nil, errors.New("reference and candidate backends must be different")
	}
	if candidate.targetsListener(config.Listen) {
		return nil, errors.New("candidate backend must not target the ingress listener")
	}
	if reference.targetsListener(config.Listen) {
		return nil, errors.New("reference backend must not target the ingress listener")
	}
	idle := make(chan struct{})
	close(idle)
	handler := &Handler{
		reference:     newReverseProxy(reference, transport, log, false),
		candidate:     newReverseProxy(candidate, transport, log, true),
		config:        config,
		log:           log,
		buffer:        newBufferBudget(config.CandidateBufferBytes),
		requests:      make(chan struct{}, config.MaxInFlightRequests),
		descriptors:   descriptors,
		comparator:    comparator,
		candidateRuns: make(map[*candidateRun]struct{}),
		idle:          idle,
	}
	requestHandler := logging.New(http.HandlerFunc(handler.serveProxy), log)
	handler.health = health.New(requestHandler)
	return handler, nil
}

// ServeHTTP serves health checks or mirrors a request to both backends.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	h.health.ServeHTTP(writer, request)
}

func (h *Handler) serveProxy(writer http.ResponseWriter, request *http.Request) {
	select {
	case h.requests <- struct{}{}:
		defer func() { <-h.requests }()
	default:
		http.Error(writer, "ingress request capacity exceeded", http.StatusServiceUnavailable)
		return
	}

	// Candidate work may outlive reference delivery, but its own timeout bounds its lifetime.
	candidateContext := context.WithoutCancel(request.Context())
	candidateContext, cancel := context.WithTimeout(candidateContext, h.config.CandidateTimeout)
	candidateRun, capacityExceeded := h.startCandidate(cancel)
	if capacityExceeded {
		h.quarantine(candidateContext, errors.New("candidate concurrency limit exceeded"))
	}
	var candidateBody *streamBody
	// A buffered handoff keeps candidate timeout from blocking the reference path.
	referenceResponse := make(chan comparison.Response, 1)
	if candidateRun != nil {
		candidateBody = newStreamBody(h.buffer, func(err error) {
			h.quarantine(candidateContext, err)
		})
		candidateRequest := request.Clone(candidateContext)
		candidateRequest.Body = candidateBody
		candidateRequest.GetBody = nil
		if request.Body == nil || request.Body == http.NoBody {
			candidateBody.closeWriter(io.EOF)
		}
		go func() {
			defer cancel()
			defer h.finishCandidate(candidateRun)
			defer candidateBody.closeReader()
			candidateWriter := newCaptureResponseWriter(newDiscardResponseWriter(), h.comparator.MaxResponseBytes())
			h.candidate.ServeHTTP(candidateWriter, candidateRequest)
			// The candidate run owns comparison so its existing limits also bound this work.
			select {
			case reference := <-referenceResponse:
				result := h.comparator.Compare(
					candidateContext,
					request.URL.Path,
					request.Header.Get("Content-Type"),
					reference,
					candidateWriter.Response(),
				)
				h.handleComparison(candidateContext, result)
			case <-candidateContext.Done():
				h.log.WarnContext(context.WithoutCancel(candidateContext), "Response comparison did not finish", "error", candidateContext.Err())
			}
		}()
	} else {
		cancel()
	}

	referenceRequest := request.Clone(request.Context())
	if candidateRun != nil && request.Body != nil && request.Body != http.NoBody {
		referenceRequest.Body = newMirrorBody(request.Body, candidateBody, cancel)
	}
	referenceRequest.GetBody = nil
	if candidateRun == nil {
		h.reference.ServeHTTP(writer, referenceRequest)
		return
	}
	referenceWriter := newCaptureResponseWriter(writer, h.comparator.MaxResponseBytes())
	h.reference.ServeHTTP(referenceWriter, referenceRequest)
	referenceResponse <- referenceWriter.Response()
}

// handleComparison applies the fail-closed policy for divergence and comparison failure.
func (h *Handler) handleComparison(ctx context.Context, result comparison.Result) {
	switch result.Outcome() {
	case comparison.Equivalent:
	case comparison.Skipped:
		h.log.DebugContext(ctx, "Response comparison skipped", "reason", result.Reason())
	case comparison.Divergent:
		h.quarantine(ctx, errors.Errorf("response differences: %s", strings.Join(result.Differences(), ", ")))
	case comparison.Unable:
		h.quarantine(ctx, errors.Errorf("response comparison failed: %s", result.Reason()))
	default:
		h.quarantine(ctx, errors.Errorf("response comparison returned unknown outcome %q", result.Outcome()))
	}
}

// Shutdown stops accepting candidate work and waits for active mirrors to finish.
func (h *Handler) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	h.closing = true
	idle := h.idle
	runs := make([]*candidateRun, 0, len(h.candidateRuns))
	for run := range h.candidateRuns {
		runs = append(runs, run)
	}
	h.mu.Unlock()
	// Candidate work is non-critical and must not extend server shutdown.
	for _, run := range runs {
		run.cancel()
	}

	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "wait for candidate requests")
	}
}

func (h *Handler) startCandidate(cancel context.CancelFunc) (*candidateRun, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing || h.quarantined {
		return nil, false
	}
	if len(h.candidateRuns) >= h.config.CandidateMaxInFlight {
		return nil, true
	}
	// Replacing the closed channel before registration keeps Shutdown's snapshot valid.
	if len(h.candidateRuns) == 0 {
		h.idle = make(chan struct{})
	}
	run := newCandidateRun(cancel)
	h.candidateRuns[run] = struct{}{}
	return run, false
}

func (h *Handler) finishCandidate(run *candidateRun) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.candidateRuns, run)
	if len(h.candidateRuns) == 0 {
		close(h.idle)
	}
}

func (h *Handler) quarantine(ctx context.Context, reason error) {
	// Quarantine is process-lifetime state. Publish it before cancelling active
	// requests so concurrent admission cannot send more traffic to the candidate.
	h.mu.Lock()
	if h.quarantined {
		h.mu.Unlock()
		return
	}
	h.quarantined = true
	runs := make([]*candidateRun, 0, len(h.candidateRuns))
	for run := range h.candidateRuns {
		runs = append(runs, run)
	}
	h.mu.Unlock()

	// Cancellation and logging may acquire transport or output locks, so keep
	// them off the reference request path that detected the quarantine.
	go func() {
		for _, run := range runs {
			run.cancel()
		}
		h.log.ErrorContext(context.WithoutCancel(ctx), "Candidate quarantined", "reason", reason)
	}()
}

func newReverseProxy(target *backendURL, transport http.RoundTripper, log *slog.Logger, discard bool) *httputil.ReverseProxy {
	if configured, ok := transport.(*Transport); ok {
		transport = configured.forBackend(target.usesH2C())
	}
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target.target())
			for name := range request.Out.Header {
				if strings.HasPrefix(http.CanonicalHeaderKey(name), "X-Forwarded-") {
					request.Out.Header.Del(name)
				}
			}
			request.Out.Header.Del("X-Real-IP")
			request.SetXForwarded()
			// The inbound Host is untrusted and must not become backend routing input.
			request.Out.Header.Del("X-Forwarded-Host")
		},
		Transport: transport,
		ErrorLog:  slog.NewLogLogger(log.Handler(), slog.LevelError),
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
			if discard {
				log.WarnContext(request.Context(), "Candidate request failed", "error", err)
				return
			}
			log.ErrorContext(request.Context(), "Reference request failed", "error", err)
			writer.WriteHeader(http.StatusBadGateway)
		},
	}
}

// backendURL retains the canonical endpoint facts used by routing safety checks.
type backendURL struct {
	url  *url.URL
	h2c  bool
	port uint16
}

func parseBackendURL(value string) (*backendURL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, errors.Wrap(err, "parse backend URL")
	}
	h2c := parsed.Scheme == "h2c"
	if (parsed.Scheme != "http" && parsed.Scheme != "https" && !h2c) || parsed.Host == "" {
		return nil, errors.Errorf("backend URL must be an absolute HTTP, HTTPS, or h2c URL: %q", value)
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.RawQuery != "" || parsed.ForceQuery {
		return nil, errors.Errorf("backend URL cannot contain user information, a query, or a fragment: %q", value)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.Errorf("backend URL cannot contain a path: %q", value)
	}
	parsed.Path = ""
	if h2c {
		parsed.Scheme = "http"
	}
	var port uint16
	portText := parsed.Port()
	if portText == "" {
		if strings.HasSuffix(parsed.Host, ":") {
			return nil, errors.Errorf("backend URL has an invalid port: %q", value)
		}
		if parsed.Scheme == "https" {
			port = 443
		} else {
			port = 80
		}
	} else {
		portNumber, err := strconv.ParseUint(portText, 10, 16)
		if err != nil || portNumber == 0 {
			return nil, errors.Errorf("backend URL has an invalid port: %q", value)
		}
		port = uint16(portNumber)
	}
	return &backendURL{url: parsed, h2c: h2c, port: port}, nil
}

func (u *backendURL) target() *url.URL {
	return u.url
}

func (u *backendURL) usesH2C() bool {
	return u.h2c
}

func (u *backendURL) identity() string {
	host := strings.ToLower(u.url.Hostname())
	if address, err := netip.ParseAddr(host); err == nil {
		host = address.Unmap().String()
	}
	return strings.ToLower(u.url.Scheme) + "://" + net.JoinHostPort(host, strconv.Itoa(int(u.port)))
}

func (u *backendURL) isLoopback() bool {
	address, err := netip.ParseAddr(u.url.Hostname())
	return err == nil && address.Unmap().IsLoopback()
}

func (u *backendURL) targetsListener(listener string) bool {
	host, port, err := net.SplitHostPort(listener)
	if err != nil {
		return false
	}
	var portNumber uint16
	if number, err := strconv.ParseUint(port, 10, 16); err == nil {
		portNumber = uint16(number)
	} else if number, err := net.DefaultResolver.LookupPort(context.Background(), "tcp", port); err == nil && number >= 0 && number <= 65535 {
		portNumber = uint16(number)
	}
	if portNumber != u.port {
		return false
	}
	target, targetError := netip.ParseAddr(u.url.Hostname())
	targetIsLocalhost := strings.EqualFold(u.url.Hostname(), "localhost")
	if targetError != nil && !targetIsLocalhost {
		return false
	}
	if host == "" {
		return true
	}
	listenAddress, err := netip.ParseAddr(host)
	if err != nil {
		return strings.EqualFold(host, "localhost") && (targetIsLocalhost || target.Unmap().IsLoopback())
	}
	if targetIsLocalhost {
		return listenAddress.IsUnspecified() || listenAddress.Unmap().IsLoopback()
	}
	return listenAddress.IsUnspecified() || listenAddress.Unmap() == target.Unmap()
}

// discardResponseWriter provides the candidate proxy sink beneath bounded capture.
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

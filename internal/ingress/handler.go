// Package ingress mirrors inbound HTTP requests to reference and candidate services.
package ingress

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/middleware/health"
	"github.com/block/spectre/internal/middleware/logging"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/proxy"
)

// Handler returns reference responses without waiting for candidate comparison.
// Every candidate run is tracked so quarantine and shutdown can cancel it.
type Handler struct {
	reference   *httputil.ReverseProxy
	candidate   *httputil.ReverseProxy
	config      Config
	log         *slog.Logger
	buffer      *proxy.Budget
	requests    chan struct{}
	health      *health.Handler
	descriptors DescriptorLoader
	static      *descriptorpb.FileDescriptorSet
	comparator  ResponseComparator
	candidates  *proxy.Candidates
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
		requestMethod string,
		requestPath string,
		requestContentType string,
		reference comparison.Response,
		candidate comparison.Response,
	) comparison.Result
}

// New constructs an ingress handler from its parsed configuration.
func New(
	config Config,
	transport http.RoundTripper,
	loader DescriptorLoader,
	comparator ResponseComparator,
	log *slog.Logger,
) (*Handler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("transport is required")
	}
	if config.Reflection && loader == nil {
		return nil, errors.New("descriptor loader is required")
	}
	if comparator == nil {
		return nil, errors.New("response comparator is required")
	}
	if log == nil {
		return nil, errors.New("logger is required")
	}
	reference, err := netaddr.ParseBackend(config.Reference)
	if err != nil {
		return nil, errors.Wrap(err, "parse reference backend")
	}
	candidate, err := netaddr.ParseBackend(config.Candidate)
	if err != nil {
		return nil, errors.Wrap(err, "parse candidate backend")
	}
	if !candidate.IsUnix() && !candidate.IsLoopback() {
		return nil, errors.New("candidate backend must use a loopback IP address or a unix socket")
	}
	if reference.SameDestination(candidate) {
		return nil, errors.New("reference and candidate backends must be different")
	}
	listen := netaddr.ParseListen(config.Listen)
	if candidate.TargetsListener(listen) {
		return nil, errors.New("candidate backend must not target the ingress listener")
	}
	if reference.TargetsListener(listen) {
		return nil, errors.New("reference backend must not target the ingress listener")
	}
	static, err := descriptors.LoadDescriptorSets(config.Descriptors)
	if err != nil {
		return nil, errors.Wrap(err, "load static descriptors")
	}
	handler := &Handler{
		reference:   proxy.NewReverseProxy(reference, transport, log, false),
		candidate:   proxy.NewReverseProxy(candidate, transport, log, true),
		config:      config,
		log:         log,
		buffer:      proxy.NewBudget(config.CandidateBufferBytes),
		requests:    make(chan struct{}, config.MaxInFlightRequests),
		descriptors: loader,
		static:      static,
		comparator:  comparator,
		candidates:  proxy.NewCandidates(config.CandidateMaxInFlight, log),
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
	candidateRun := h.candidates.Start(candidateContext, cancel)
	var candidateBody *streamBody
	// A buffered handoff keeps candidate timeout from blocking the reference path.
	referenceResponse := make(chan comparison.Response, 1)
	if candidateRun != nil {
		candidateBody = newStreamBody(h.buffer, func(err error) {
			h.candidates.Quarantine(candidateContext, err)
		})
		candidateRequest := request.Clone(candidateContext)
		candidateRequest.Body = candidateBody
		candidateRequest.GetBody = nil
		if request.Body == nil || request.Body == http.NoBody {
			candidateBody.closeWriter(io.EOF)
		}
		go func() {
			defer cancel()
			defer h.candidates.Finish(candidateRun)
			defer candidateBody.closeReader()
			candidateWriter := newCaptureResponseWriter(newDiscardResponseWriter(), h.comparator.MaxResponseBytes())
			h.candidate.ServeHTTP(candidateWriter, candidateRequest)
			// The candidate run owns comparison so its existing limits also bound this work.
			select {
			case reference := <-referenceResponse:
				result := h.comparator.Compare(
					candidateContext,
					request.Method,
					request.URL.EscapedPath(),
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
		h.candidates.Quarantine(ctx, errors.Errorf("response differences: %s", strings.Join(result.Differences(), ", ")))
	case comparison.Unable:
		h.candidates.Quarantine(ctx, errors.Errorf("response comparison failed: %s", result.Reason()))
	default:
		h.candidates.Quarantine(ctx, errors.Errorf("response comparison returned unknown outcome %q", result.Outcome()))
	}
}

// Shutdown stops accepting candidate work and waits for active mirrors to finish.
func (h *Handler) Shutdown(ctx context.Context) error {
	return errors.WithStack(h.candidates.Shutdown(ctx))
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

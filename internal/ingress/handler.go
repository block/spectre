// Package ingress mirrors inbound HTTP requests to reference and candidate services.
package ingress

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/alecthomas/errors"
)

// Handler forwards requests to both backends and returns only the reference response.
type Handler struct {
	reference        *httputil.ReverseProxy
	candidate        *httputil.ReverseProxy
	candidateTimeout time.Duration

	mu       sync.Mutex
	closing  bool
	inFlight int
	idle     chan struct{}
}

// New constructs an ingress handler for the reference and candidate backends.
func New(reference, candidate *url.URL, candidateTimeout time.Duration, transport http.RoundTripper, log *slog.Logger) *Handler {
	idle := make(chan struct{})
	close(idle)
	return &Handler{reference: newReverseProxy(reference, transport, log, false), candidate: newReverseProxy(candidate, transport, log, true), candidateTimeout: candidateTimeout, idle: idle}
}

// ServeHTTP mirrors a request without allowing candidate work to delay the response.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	candidateContext := context.WithoutCancel(request.Context())
	var cancel context.CancelFunc
	if h.candidateTimeout > 0 {
		candidateContext, cancel = context.WithTimeout(candidateContext, h.candidateTimeout)
	} else {
		candidateContext, cancel = context.WithCancel(candidateContext)
	}
	candidateBody := newStreamBody()
	candidateRequest := request.Clone(candidateContext)
	candidateRequest.Body = candidateBody
	candidateRequest.GetBody = nil
	candidateStarted := h.startCandidate()
	if candidateStarted {
		go func() {
			defer cancel()
			defer h.finishCandidate()
			defer candidateBody.closeReader()
			h.candidate.ServeHTTP(newDiscardResponseWriter(), candidateRequest)
		}()
	} else {
		cancel()
		candidateBody.closeReader()
	}

	referenceRequest := request.Clone(request.Context())
	if candidateStarted {
		referenceRequest.Body = newMirrorBody(request.Body, candidateBody)
	}
	referenceRequest.GetBody = nil
	h.reference.ServeHTTP(writer, referenceRequest)
}

// Shutdown stops accepting candidate work and waits for active mirrors to finish.
func (h *Handler) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	h.closing = true
	idle := h.idle
	h.mu.Unlock()

	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "wait for candidate requests")
	}
}

func (h *Handler) startCandidate() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return false
	}
	// Replacing the closed channel before incrementing keeps Shutdown's snapshot valid.
	if h.inFlight == 0 {
		h.idle = make(chan struct{})
	}
	h.inFlight++
	return true
}

func (h *Handler) finishCandidate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inFlight--
	if h.inFlight == 0 {
		close(h.idle)
	}
}

func newReverseProxy(target *url.URL, transport http.RoundTripper, log *slog.Logger, discard bool) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	proxy.ErrorLog = slog.NewLogLogger(log.Handler(), slog.LevelError)
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
		if discard {
			log.WarnContext(request.Context(), "Candidate request failed", "error", err)
			return
		}
		log.ErrorContext(request.Context(), "Reference request failed", "error", err)
		writer.WriteHeader(http.StatusBadGateway)
	}
	return proxy
}

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

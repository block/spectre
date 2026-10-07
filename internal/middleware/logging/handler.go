// Package logging provides structured HTTP request logging middleware.
package logging

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// Demoter reports whether a request's log line should drop from info to debug level.
type Demoter func(request *http.Request) bool

// Handler logs each request after delegating it to the next handler.
type Handler struct {
	next  http.Handler
	log   *slog.Logger
	event string
	debug Option[Demoter]
}

// New constructs request logging middleware around the next handler. event names
// the SPECTRE event each request is logged under, e.g. "ingress_received".
func New(next http.Handler, log *slog.Logger, event string) *Handler {
	return &Handler{next: next, log: log, event: event, debug: None[Demoter]()}
}

// NewDemoting is like New but logs a request at debug instead of info when demote
// reports true, keeping low-value traffic such as health checks quiet by default.
func NewDemoting(next http.Handler, log *slog.Logger, event string, demote Demoter) *Handler {
	return &Handler{next: next, log: log, event: event, debug: Some(demote)}
}

// ServeHTTP logs the event, method, path, response status, and elapsed time.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	start := time.Now()
	response := newResponseWriter(writer)
	h.next.ServeHTTP(response, request)
	status := response.statusCode()
	if status == 0 {
		status = http.StatusOK
	}
	level := slog.LevelInfo
	if demote, ok := h.debug.Get(); ok && demote(request) {
		level = slog.LevelDebug
	}
	h.log.Log(request.Context(), level, "HTTP request",
		"event", h.event,
		"method", request.Method,
		"path", request.URL.Path,
		"status", status,
		"duration", time.Since(start),
	)
}

// responseWriter records the first committed status while preserving optional HTTP APIs.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func newResponseWriter(writer http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: writer}
}

func (w *responseWriter) statusCode() int {
	return w.status
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	written, err := w.ResponseWriter.Write(data)
	return written, errors.Wrap(err, "write HTTP response")
}

// Flush preserves streaming support required by gRPC and reverse proxies.
func (w *responseWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

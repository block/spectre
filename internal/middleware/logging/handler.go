// Package logging provides structured HTTP request logging middleware.
package logging

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/alecthomas/errors"
)

// Handler logs each request after delegating it to the next handler.
type Handler struct {
	next http.Handler
	log  *slog.Logger
}

// New constructs request logging middleware around the next handler.
func New(next http.Handler, log *slog.Logger) *Handler {
	return &Handler{next: next, log: log}
}

// ServeHTTP logs the method, path, response status, and elapsed time.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	start := time.Now()
	response := newResponseWriter(writer)
	h.next.ServeHTTP(response, request)
	status := response.statusCode()
	if status == 0 {
		status = http.StatusOK
	}
	h.log.InfoContext(request.Context(), "HTTP request",
		"method", request.Method,
		"path", request.URL.Path,
		"status", status,
		"duration", time.Since(start),
	)
}

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

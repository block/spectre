// Package health provides HTTP liveness and readiness middleware.
package health

import (
	"net/http"
	"sync/atomic"
)

const (
	livenessPath  = "/livez"
	readinessPath = "/readyz"
)

// Handler serves health endpoints and delegates all other requests.
type Handler struct {
	next  http.Handler
	ready atomic.Bool
}

// New constructs health middleware around the next request handler.
func New(next http.Handler) *Handler {
	return &Handler{next: next}
}

// SetReady changes whether the readiness endpoint reports success.
func (h *Handler) SetReady(ready bool) {
	h.ready.Store(ready)
}

// ServeHTTP serves health endpoints and delegates all other requests.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	var healthy bool
	switch request.URL.Path {
	case livenessPath:
		healthy = true
	case readinessPath:
		healthy = h.ready.Load()
	default:
		h.next.ServeHTTP(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !healthy {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

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
// Readiness is atomic because startup and shutdown race with request handling.
type Handler struct {
	next  http.Handler
	ready atomic.Bool
	// dependencies must all be ready for this handler to be ready.
	dependencies []*Handler
}

// New constructs health middleware around the next request handler.
func New(next http.Handler) *Handler {
	return &Handler{next: next}
}

// NewAll serves health endpoints that are ready while every one of handlers is,
// and answers 404 to everything else.
func NewAll(handlers ...*Handler) *Handler {
	all := &Handler{next: http.NotFoundHandler(), dependencies: handlers}
	all.SetReady(true)
	return all
}

// SetReady changes whether the readiness endpoint reports success.
func (h *Handler) SetReady(ready bool) {
	h.ready.Store(ready)
}

// Ready reports whether the readiness endpoint reports success.
func (h *Handler) Ready() bool {
	if !h.ready.Load() {
		return false
	}
	for _, dependency := range h.dependencies {
		if !dependency.Ready() {
			return false
		}
	}
	return true
}

// ServeHTTP serves health endpoints and delegates all other requests.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	var healthy bool
	switch request.URL.Path {
	case livenessPath:
		healthy = true
	case readinessPath:
		healthy = h.Ready()
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

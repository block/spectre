// Package route matches HTTP requests against net/http.ServeMux patterns.
package route

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/alecthomas/errors"
)

// Map assigns a value to each pattern. Build it with Add before sharing it;
// concurrent Match calls are safe once construction is complete.
type Map[T any] struct {
	mux *http.ServeMux
}

// valueHandler lets ServeMux perform matching; it is never served.
type valueHandler[T any] struct {
	value T
}

func newValueHandler[T any](value T) valueHandler[T] {
	return valueHandler[T]{value: value}
}

func (h valueHandler[T]) assigned() T {
	return h.value
}

func (h valueHandler[T]) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	_, _ = writer, request
}

// New returns an empty map.
func New[T any]() *Map[T] {
	return &Map[T]{mux: http.NewServeMux()}
}

// Add assigns a value to a "<METHOD> /<path>" pattern. Patterns must name an
// HTTP method and must not include a host.
func (m *Map[T]) Add(pattern string, value T) error {
	fields := strings.Fields(pattern)
	if len(fields) != 2 {
		return errors.Errorf("route pattern %q must have the form \"<METHOD> /<path>\"", pattern)
	}
	if !strings.HasPrefix(fields[1], "/") {
		return errors.Errorf("route pattern %q must not include a host", pattern)
	}
	return register(m.mux, pattern, newValueHandler(value))
}

// register converts ServeMux's panics for invalid or conflicting patterns into errors.
func register(mux *http.ServeMux, pattern string, handler http.Handler) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = errors.Errorf("invalid route pattern: %s", fmt.Sprint(recovered))
		}
	}()
	mux.Handle(pattern, handler)
	return nil
}

// Match returns the value assigned to a request method and path. Unlike ServeMux,
// a GET pattern does not match HEAD, whose empty responses need their own pattern.
func (m *Map[T]) Match(requestMethod, requestPath string) (value T, matched bool) {
	request := &http.Request{Method: requestMethod, URL: &url.URL{Path: requestPath}}
	// Unmatched, redirected, and wrong-method requests get ServeMux's own handlers.
	handler, pattern := m.mux.Handler(request)
	route, matched := handler.(valueHandler[T])
	if !matched || strings.Fields(pattern)[0] != requestMethod {
		var unmatched T
		return unmatched, false
	}
	return route.assigned(), true
}

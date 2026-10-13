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

// valueHandler lets ServeMux perform matching. It only serves matchWriter.
type valueHandler[T any] struct {
	value     T
	wildcards []string
}

func newValueHandler[T any](value T, wildcards []string) valueHandler[T] {
	return valueHandler[T]{value: value, wildcards: wildcards}
}

func (h valueHandler[T]) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if match, ok := writer.(*matchWriter[T]); ok {
		match.record(h.value, h.wildcards, request)
	}
}

// matchWriter receives the matched value, because only ServeMux.ServeHTTP binds
// wildcards. ServeMux's own handlers write their redirect or error responses to it.
type matchWriter[T any] struct {
	header    http.Header
	value     T
	wildcards map[string]string
	pattern   string
}

func newMatchWriter[T any]() *matchWriter[T] {
	return &matchWriter[T]{header: http.Header{}}
}

func (w *matchWriter[T]) record(value T, wildcards []string, request *http.Request) {
	w.value = value
	w.pattern = request.Pattern
	w.wildcards = make(map[string]string, len(wildcards))
	for _, name := range wildcards {
		w.wildcards[name] = request.PathValue(name)
	}
}

// matched returns the recorded value and wildcards, if a pattern for the method matched.
func (w *matchWriter[T]) matched(requestMethod string) (value T, wildcards map[string]string, matched bool) {
	if w.pattern == "" || strings.Fields(w.pattern)[0] != requestMethod {
		var unmatched T
		return unmatched, nil, false
	}
	return w.value, w.wildcards, true
}

func (w *matchWriter[T]) Header() http.Header {
	return w.header
}

func (w *matchWriter[T]) Write(data []byte) (int, error) {
	return len(data), nil
}

func (w *matchWriter[T]) WriteHeader(statusCode int) {
	_ = statusCode
}

// New returns an empty map.
func New[T any]() *Map[T] {
	return &Map[T]{mux: http.NewServeMux()}
}

// Add assigns a value to a "<METHOD> [<host>]/<path>" pattern, which must name an
// HTTP method. A pattern without a host matches every host.
func (m *Map[T]) Add(pattern string, value T) error {
	if len(strings.Fields(pattern)) != 2 {
		return errors.Errorf("route pattern %q must have the form \"<METHOD> [<host>]/<path>\"", pattern)
	}
	return register(m.mux, pattern, newValueHandler(value, Wildcards(pattern)))
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

// Match returns a request's value and wildcards; requestPath must be escaped.
// Unlike ServeMux, a GET pattern does not match HEAD, whose empty responses need their own pattern.
func (m *Map[T]) Match(requestMethod, requestHost, requestPath string) (value T, wildcards map[string]string, matched bool) {
	var unmatched T
	path, err := url.PathUnescape(requestPath)
	if err != nil {
		return unmatched, nil, false
	}
	// ServeMux matches the escaped path, so an escaped slash stays within one segment.
	request := &http.Request{Method: requestMethod, Host: requestHost, URL: &url.URL{Path: path, RawPath: requestPath}}
	match := newMatchWriter[T]()
	m.mux.ServeHTTP(match, request)
	return match.matched(requestMethod)
}

// Wildcards returns the names of a valid pattern's path wildcards in order.
func Wildcards(pattern string) []string {
	fields := strings.Fields(pattern)
	target := fields[len(fields)-1]
	names := []string{}
	for segment := range strings.SplitSeq(target[strings.Index(target, "/")+1:], "/") {
		inner, isWildcard := strings.CutPrefix(segment, "{")
		inner, closed := strings.CutSuffix(inner, "}")
		if !isWildcard || !closed || inner == "$" {
			continue
		}
		names = append(names, strings.TrimSuffix(inner, "..."))
	}
	return names
}

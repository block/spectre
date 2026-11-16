package integration

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// Recorder counts the requests a service or dependency receives, by path. It is
// safe for concurrent use.
type Recorder struct {
	name  string
	next  http.Handler
	watch *watch
	mu    sync.Mutex
	paths []string
}

func newRecorder(name string, next http.Handler, watch *watch) *Recorder {
	return &Recorder{name: name, next: next, watch: watch}
}

// ServeHTTP records the request's path as it arrives, then serves it.
func (r *Recorder) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	r.mu.Lock()
	r.paths = append(r.paths, request.URL.Path)
	r.mu.Unlock()
	r.next.ServeHTTP(writer, request)
}

// Count returns how many requests for path have arrived so far.
func (r *Recorder) Count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, received := range r.paths {
		if received == path {
			count++
		}
	}
	return count
}

// WaitFor waits until at least count requests for path have arrived.
func (r *Recorder) WaitFor(t *testing.T, path string, count int) {
	t.Helper()
	r.watch.await(t, waitTimeout, fmt.Sprintf("%d requests for %s to reach %s", count, path, r.name), func() bool {
		return r.Count(path) >= count
	})
}

package health_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/middleware/health"
)

func TestHealthEndpoints(t *testing.T) {
	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusCreated)
	})
	handler := health.New(next)
	for _, test := range []struct {
		name   string
		method string
		path   string
		status int
		allow  string
	}{
		{name: "Liveness", method: http.MethodGet, path: "/livez", status: http.StatusNoContent},
		{name: "NotReady", method: http.MethodGet, path: "/readyz", status: http.StatusServiceUnavailable},
		{name: "Head", method: http.MethodHead, path: "/livez", status: http.StatusNoContent},
		{name: "MethodNotAllowed", method: http.MethodPost, path: "/livez", status: http.StatusMethodNotAllowed, allow: "GET, HEAD"},
		{name: "Delegates", method: http.MethodGet, path: "/users", status: http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			assert.Equal(t, test.status, response.Code)
			assert.Equal(t, test.allow, response.Header().Get("Allow"))
		})
	}

	handler.SetReady(true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusNoContent, response.Code)
	handler.SetReady(false)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
}

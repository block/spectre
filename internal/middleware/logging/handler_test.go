package logging_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/middleware/logging"
)

func TestLogsRequests(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
	}{
		{name: "ImplicitOK", status: http.StatusOK},
		{name: "ExplicitStatus", status: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&output, nil))
			handler := logging.New(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if test.status == http.StatusOK {
					_, err := writer.Write([]byte("ok"))
					assert.NoError(t, err)
					return
				}
				writer.WriteHeader(test.status)
			}), log, "ingress_received")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/users?id=1", nil))

			var record map[string]any
			assert.NoError(t, json.Unmarshal(output.Bytes(), &record))
			assert.Equal(t, "INFO", record["level"])
			assert.Equal(t, "HTTP request", record["msg"])
			assert.Equal(t, "ingress_received", record["event"])
			assert.Equal(t, http.MethodPost, record["method"])
			assert.Equal(t, "/users", record["path"])
			status, hasStatus := record["status"].(float64)
			assert.True(t, hasStatus)
			assert.Equal(t, float64(test.status), status)
			_, hasDuration := record["duration"].(float64)
			assert.True(t, hasDuration)
		})
	}
}

func TestDemotesMatchingRequests(t *testing.T) {
	for _, test := range []struct {
		name  string
		path  string
		level string
	}{
		{name: "Ignored", path: "/_status", level: "DEBUG"},
		{name: "Compared", path: "/users", level: "INFO"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
			handler := logging.NewDemoting(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, err := writer.Write([]byte("ok"))
				assert.NoError(t, err)
			}), log, "ingress_received", func(request *http.Request) bool {
				return request.URL.Path == "/_status"
			})
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, test.path, nil))

			var record map[string]any
			assert.NoError(t, json.Unmarshal(output.Bytes(), &record))
			level, _ := record["level"].(string)
			assert.Equal(t, test.level, level)
			assert.Equal(t, "HTTP request", record["msg"])
			assert.Equal(t, "ingress_received", record["event"])
		})
	}
}

func TestPreservesFlushing(t *testing.T) {
	response := httptest.NewRecorder()
	log := slog.New(slog.DiscardHandler)
	handler := logging.New(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.(http.Flusher).Flush()
	}), log, "egress_received")
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/stream", nil))
	assert.True(t, response.Flushed)
	assert.Equal(t, http.StatusOK, response.Code)
}

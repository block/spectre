package status_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/status"
)

// fakeReporter is a static quarantine source.
type fakeReporter struct {
	quarantine proxy.QuarantineStatus
}

func (f fakeReporter) QuarantineStatus() proxy.QuarantineStatus { return f.quarantine }

func TestStatusReport(t *testing.T) {
	since := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	handler := status.NewHandler(time.Now(),
		status.Source{Name: "ingress", Reporter: fakeReporter{}},
		status.Source{Name: "egress", Reporter: fakeReporter{proxy.QuarantineStatus{
			Reason: errors.New("GetUnit is not declared"),
			Since:  since,
		}}},
	)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, status.Path, nil))

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "application/json", response.Header().Get("Content-Type"))

	var report status.Report
	assert.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	assert.True(t, report.Quarantined)
	assert.False(t, report.HealthyForSeconds.Ok())
	assert.Equal(t, 2, len(report.Sources))

	ingress := report.Sources[0]
	assert.Equal(t, "ingress", ingress.Name)
	assert.False(t, ingress.Quarantined)
	assert.Equal(t, "", ingress.Reason)
	assert.Equal(t, "", ingress.Since)

	egress := report.Sources[1]
	assert.Equal(t, "egress", egress.Name)
	assert.True(t, egress.Quarantined)
	assert.Equal(t, "GetUnit is not declared", egress.Reason)
	assert.Equal(t, since.Format(time.RFC3339), egress.Since)
}

func TestStatusHealthy(t *testing.T) {
	handler := status.NewHandler(time.Now().Add(-90*time.Minute), status.Source{Name: "ingress", Reporter: fakeReporter{}})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, status.Path, nil))

	var report status.Report
	assert.NoError(t, json.Unmarshal(response.Body.Bytes(), &report))
	assert.False(t, report.Quarantined)
	healthyFor, ok := report.HealthyForSeconds.Get()
	assert.True(t, ok)
	assert.True(t, healthyFor >= 90*60, "healthy for %ds", healthyFor)
	assert.Equal(t, "", report.Sources[0].Since)
}

func TestStatusRequests(t *testing.T) {
	handler := status.NewHandler(time.Now())
	for _, test := range []struct {
		name   string
		method string
		path   string
		status int
		allow  string
	}{
		{name: "WrongPath", method: http.MethodGet, path: "/other", status: http.StatusNotFound},
		{name: "MethodNotAllowed", method: http.MethodPost, path: status.Path, status: http.StatusMethodNotAllowed, allow: "GET, HEAD"},
		{name: "Head", method: http.MethodHead, path: status.Path, status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			assert.Equal(t, test.status, response.Code)
			assert.Equal(t, test.allow, response.Header().Get("Allow"))
			if test.method == http.MethodHead {
				assert.Equal(t, "", response.Body.String())
			}
		})
	}
}

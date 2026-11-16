package integration_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/integration"
	samplepb "github.com/block/spectre/internal/sample/pb"
)

const (
	listUsersPath = "/spectre.sample.v1.UserService/ListUsers"
	getUserPath   = "/spectre.sample.v1.UserService/GetUser"
)

func TestNormalisedResponsesAreEquivalent(t *testing.T) {
	t.Parallel()
	m := startSampleMirror(t)
	users := users(m)

	listUserOne(t, users)
	m.Candidate.WaitFor(t, listUsersPath, 1)
	m.Ingress.WaitFor(t, integration.Fields{"msg": "Payload normaliser completed", "kind": "field", "target": "spectre.sample.v1.User.roles"})
	m.Ingress.WaitFor(t, integration.Fields{"target": "spectre.sample.v1.ListUsersResponse.generatedAt", "side": "candidate", "payload_path": "$.generatedAt"})
	m.Ingress.WaitFor(t, comparison("POST "+listUsersPath, "equivalent"))

	// Past the match window, a second mirrored request shows the reordered roles
	// did not quarantine the candidate.
	time.Sleep(1100 * time.Millisecond)
	listUserOne(t, users)
	m.Candidate.WaitFor(t, listUsersPath, 2)
}

func TestRawHTTPResponsesAreEquivalent(t *testing.T) {
	t.Parallel()
	m := startSampleMirror(t)

	// Raw HTTP responses are typed by the endpoints that weather.ts declares.
	status, body := m.Get(t, m.IngressURL+"/v2/forecast?location=london")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `"alerts"`)
	m.Ingress.WaitFor(t, integration.Fields{"target": "weather.GetForecastV2Response.alerts"})
	m.Ingress.WaitFor(t, comparison("GET /v2/forecast", "equivalent"))

	for path, route := range map[string]string{
		"/api/v1/forecast?location=sydney": "GET /api/v1/forecast",
		"/_status":                         "GET /_status",
	} {
		pathStatus, _ := m.Get(t, m.IngressURL+path)
		assert.Equal(t, http.StatusOK, pathStatus)
		m.Ingress.WaitFor(t, comparison(route, "equivalent"))
	}
}

func TestEgressReplaysReferenceResponses(t *testing.T) {
	t.Parallel()
	m := startSampleMirror(t)
	locations := map[string]string{"london": "/v2/forecast", "sydney": "/api/v1/forecast"}

	for location, path := range locations {
		status, _ := m.Get(t, m.IngressURL+path+"?location="+location)
		assert.Equal(t, http.StatusOK, status)
	}

	for _, side := range []string{"reference", "candidate"} {
		m.Egress.WaitFor(t, integration.Fields{"msg": "Payload normalisation completed", "message": "forecasts.FetchForecastRequest", "side": side, "normalisers": float64(0)})
	}
	m.Egress.WaitForCount(t, integration.Fields{"msg": "Candidate request matched a reference request", "event": "correlation", "protocol": "http", "route": "GET forecasts.example/v1/forecasts/{location}"}, len(locations))
	// Egress answers the candidate with the reference's forecast, so each location
	// reaches the provider once.
	forecasts := m.Dependency(t, forecastsHost)
	for location := range locations {
		assert.Equal(t, 1, forecasts.Count("/v1/forecasts/"+location), "provider calls for %s", location)
	}
}

func TestDivergentResponseQuarantinesCandidate(t *testing.T) {
	t.Parallel()
	m := startSampleMirror(t)
	users := users(m)

	response, err := users.GetUser(t.Context(), &samplepb.GetUserRequest{Id: "user-2"})
	assert.NoError(t, err)
	assert.Equal(t, "Sam Sample", response.GetUser().GetName())
	assert.Equal(t, map[string]string{"team": "platform"}, response.GetUser().GetLabels())
	m.Candidate.WaitFor(t, getUserPath, 1)
	m.Ingress.WaitFor(t, comparison("POST "+getUserPath, "divergent"))
	m.Ingress.WaitFor(t, integration.Fields{"msg": "Candidate quarantined"})

	// The reference still answers, and the candidate receives nothing more.
	listUserOne(t, users)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 1, m.Reference.Count(listUsersPath))
	assert.Equal(t, 0, m.Candidate.Count(listUsersPath), "candidate requests after quarantine")
}

func TestUnmatchedCandidateEgressQuarantinesCandidate(t *testing.T) {
	t.Parallel()
	m := startSampleMirror(t)
	forecast := func(location string) int {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, m.CandidateEgressURL+"/v1/forecasts/"+location, nil)
		assert.NoError(t, err)
		request.Host = forecastsHost
		status, _ := m.Do(t, request)
		return status
	}

	// A dependency call the reference never made gets a 502 without reaching the provider.
	assert.Equal(t, http.StatusBadGateway, forecast("paris"))
	m.Egress.WaitFor(t, integration.Fields{"msg": "Candidate quarantined"})
	assert.Equal(t, http.StatusServiceUnavailable, forecast("london"))
	assert.Equal(t, 0, m.Dependency(t, forecastsHost).Count("/v1/forecasts/paris"))
}

// comparison selects the record ingress logs when it finishes comparing a route.
func comparison(route string, outcome string) integration.Fields {
	return integration.Fields{"msg": "Response comparison completed", "event": "correlation", "protocol": "http", "route": route, "outcome": outcome}
}

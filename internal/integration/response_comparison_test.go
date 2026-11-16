package integration_test

import (
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	samplepb "github.com/block/spectre/internal/sample/pb"
)

const (
	listUsersPath = "/spectre.sample.v1.UserService/ListUsers"
	listUsers     = model.Route("POST " + listUsersPath)
	getUser       = model.Route("POST /spectre.sample.v1.UserService/GetUser")
)

func TestNormalisedResponsesAreEquivalent(t *testing.T) {
	t.Parallel()
	m := startSampleMirror(t)
	users := users(m)

	// The candidate reorders roles and generates its own timestamp, which the
	// sample's scripts normalise, so each call counts and none quarantines.
	listUserOne(t, users)
	waitForCalls(t, m, map[model.Route]uint64{listUsers: 1})
	listUserOne(t, users)
	waitForCalls(t, m, map[model.Route]uint64{listUsers: 2})
}

func TestRawHTTPResponsesAreEquivalent(t *testing.T) {
	t.Parallel()
	m := startSampleMirror(t)

	// Raw HTTP responses are typed by the endpoints that weather.ts declares,
	// whose scripts treat the candidate's reordered alerts as equivalent.
	status, body := m.Get(t, m.IngressURL+"/v2/forecast?location=london")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `"alerts"`)
	status, _ = m.Get(t, m.IngressURL+"/api/v1/forecast?location=sydney")
	assert.Equal(t, http.StatusOK, status)
	status, _ = m.Get(t, m.IngressURL+"/_status")
	assert.Equal(t, http.StatusOK, status)

	waitForCalls(t, m, map[model.Route]uint64{"GET /v2/forecast": 1, "GET /api/v1/forecast": 1, "GET /_status": 1})
}

func TestEgressReplaysReferenceResponses(t *testing.T) {
	t.Parallel()
	m := startSampleMirror(t)

	for _, path := range []string{"/v2/forecast?location=london", "/api/v1/forecast?location=sydney"} {
		status, _ := m.Get(t, m.IngressURL+path)
		assert.Equal(t, http.StatusOK, status)
	}

	waitForCalls(t, m, map[model.Route]uint64{"GET /v2/forecast": 1, "GET /api/v1/forecast": 1})
	// Egress answers the candidate with the reference's forecast, so each location
	// reaches the provider once.
	for _, location := range []string{"london", "sydney"} {
		assert.Equal(t, 1, m.forecasts.count("/v1/forecasts/"+location), "provider calls for %s", location)
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
	assert.Equal(t, model.QuarantineReason{
		Kind:    model.QuarantineDivergent,
		Traffic: Some(model.Traffic{Direction: model.Ingress, Protocol: model.ProtocolHTTP, Route: getUser}),
		// Reasons never quote values, so the label's key is a wildcard.
		Differences: []model.Difference{{Path: "$.user.labels[*]", Change: model.ChangeChanged, Count: 1}},
	}, m.WaitForQuarantine(t, model.Ingress))

	// The reference still answers. Ingress turns the candidate away before the
	// reference responds, so once ListUsers returns the candidate cannot get it.
	listUserOne(t, users)
	assert.Equal(t, 0, m.candidate.count(listUsersPath), "candidate requests after quarantine")
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
	assert.Equal(t, model.QuarantineReason{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: "GET forecasts.example/v1/forecasts/{location}"}),
		Detail:  "no matching reference request within 1s",
	}, m.WaitForQuarantine(t, model.Egress))
	assert.Equal(t, http.StatusServiceUnavailable, forecast("london"))
	assert.Equal(t, 0, m.forecasts.count("/v1/forecasts/paris"))
}

// waitForCalls waits for the proxy to report the expected compared calls, and
// checks the candidate is still healthy.
func waitForCalls(t *testing.T, m sampleMirror, expected map[model.Route]uint64) {
	t.Helper()
	state := m.WaitForCalls(t, expected)
	assert.Equal(t, map[model.Direction]model.Quarantine(nil), state.Quarantines)
	assert.Equal(t, model.Calls{Routes: expected}, state.Calls)
}

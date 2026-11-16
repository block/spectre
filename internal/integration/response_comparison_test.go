package integration_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	samplepb "github.com/block/spectre/internal/sample/pb"
	"github.com/block/spectre/internal/sample/pb/samplepbconnect"
)

// listUsers selects the candidate's log of serving ListUsers.
var listUsers = processLog("candidate", map[string]any{"path": "/spectre.sample.v1.UserService/ListUsers"})

func TestNormalisedResponsesAreEquivalent(t *testing.T) {
	t.Parallel()
	m := startMirror(t)
	users := m.users(t)

	listUserOne(t, users)
	m.waitFor(t, listUsers)
	m.waitFor(t, ingressLog(map[string]any{"msg": "Payload normaliser completed", "kind": "field", "target": "spectre.sample.v1.User.roles"}))
	m.waitFor(t, ingressLog(map[string]any{"target": "spectre.sample.v1.ListUsersResponse.generatedAt", "side": "candidate", "payload_path": "$.generatedAt"}))
	m.waitFor(t, comparison("POST /spectre.sample.v1.UserService/ListUsers", "equivalent"))

	// Past the match window, a second mirrored request shows the reordered roles
	// did not quarantine the candidate.
	time.Sleep(1100 * time.Millisecond)
	listUserOne(t, users)
	m.waitForCount(t, listUsers, 2)
}

func TestRawHTTPResponsesAreEquivalent(t *testing.T) {
	t.Parallel()
	m := startMirror(t)

	// Raw HTTP responses are typed by the endpoints that weather.ts declares.
	status, body := m.get(t, "ingress", "http://ingress/v2/forecast?location=london")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `"alerts"`)
	m.waitFor(t, ingressLog(map[string]any{"target": "weather.GetForecastV2Response.alerts"}))
	m.waitFor(t, comparison("GET /v2/forecast", "equivalent"))

	for path, route := range map[string]string{
		"/api/v1/forecast?location=sydney": "GET /api/v1/forecast",
		"/_status":                         "GET /_status",
	} {
		pathStatus, _ := m.get(t, "ingress", "http://ingress"+path)
		assert.Equal(t, http.StatusOK, pathStatus)
		m.waitFor(t, comparison(route, "equivalent"))
	}
}

func TestEgressReplaysReferenceResponses(t *testing.T) {
	t.Parallel()
	m := startMirror(t)
	locations := map[string]string{"london": "/v2/forecast", "sydney": "/api/v1/forecast"}

	for location, path := range locations {
		status, _ := m.get(t, "ingress", "http://ingress"+path+"?location="+location)
		assert.Equal(t, http.StatusOK, status)
	}

	for _, side := range []string{"reference", "candidate"} {
		m.waitFor(t, egressLog(map[string]any{"msg": "Payload normalisation completed", "message": "forecasts.FetchForecastRequest", "side": side, "normalisers": float64(0)}))
	}
	matched := egressLog(map[string]any{"msg": "Candidate request matched a reference request", "event": "correlation", "protocol": "http", "route": "GET forecasts.example/v1/forecasts/{location}"})
	m.waitForCount(t, matched, len(locations))
	// Egress answers the candidate with the reference's forecast, so each location
	// reaches the provider once.
	for location := range locations {
		assert.Equal(t, 1, m.count(processLog("forecasts", map[string]any{"path": "/v1/forecasts/" + location})), "provider calls for %s", location)
	}
}

func TestDivergentResponseQuarantinesCandidate(t *testing.T) {
	t.Parallel()
	m := startMirror(t)
	users := m.users(t)

	response, err := users.GetUser(t.Context(), &samplepb.GetUserRequest{Id: "user-2"})
	assert.NoError(t, err)
	assert.Equal(t, "Sam Sample", response.GetUser().GetName())
	assert.Equal(t, map[string]string{"team": "platform"}, response.GetUser().GetLabels())
	m.waitFor(t, processLog("candidate", map[string]any{"path": "/spectre.sample.v1.UserService/GetUser"}))
	m.waitFor(t, comparison("POST /spectre.sample.v1.UserService/GetUser", "divergent"))
	m.waitFor(t, ingressLog(map[string]any{"msg": "Candidate quarantined"}))

	// The reference still answers, and the candidate receives nothing more.
	before := m.count(listUsers)
	listUserOne(t, users)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, before, m.count(listUsers), "candidate requests after quarantine")
}

func TestUnmatchedCandidateEgressQuarantinesCandidate(t *testing.T) {
	t.Parallel()
	m := startMirror(t)

	// A dependency call the reference never made gets a 502 without reaching the provider.
	status, _ := m.get(t, "egress-candidate", "http://forecasts.example/v1/forecasts/paris")
	assert.Equal(t, http.StatusBadGateway, status)
	m.waitFor(t, egressLog(map[string]any{"msg": "Candidate quarantined"}))
	status, _ = m.get(t, "egress-candidate", "http://forecasts.example/v1/forecasts/london")
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, 0, m.count(processLog("forecasts", map[string]any{"path": "/v1/forecasts/paris"})))
}

// comparison selects the record ingress logs when it finishes comparing a route.
func comparison(route string, outcome string) query {
	return ingressLog(map[string]any{"msg": "Response comparison completed", "event": "correlation", "protocol": "http", "route": route, "outcome": outcome})
}

// listUserOne lists user-1 through ingress, checking it gets the reference's answer.
func listUserOne(t *testing.T, users samplepbconnect.UserServiceClient) {
	t.Helper()
	response, err := users.ListUsers(t.Context(), &samplepb.ListUsersRequest{Ids: []string{"user-1"}})
	assert.NoError(t, err)
	names := []string{}
	for _, user := range response.GetUsers() {
		names = append(names, user.GetName())
	}
	assert.Equal(t, []string{"Alex Example"}, names)
}

// candidateEnv gives the candidate sample data whose user-1 roles are reordered,
// whose user-2 has another team, and whose first and last weather alerts swap.
func candidateEnv(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	users := rewrite(t, "users.json",
		`"ROLE_ADMIN", "ROLE_EDITOR"`, `"ROLE_EDITOR", "ROLE_ADMIN"`,
		`"team": "platform"`, `"team": "candidate"`)
	first := `"id": "alert-1", "severity": "moderate", "headline": "Heavy rain"`
	last := `"id": "alert-3", "severity": "minor", "headline": "Fog"`
	weather := rewrite(t, "weather.json", first, "SWAPPED", last, first, "SWAPPED", last)
	usersPath := filepath.Join(dir, "users.json")
	weatherPath := filepath.Join(dir, "weather.json")
	assert.NoError(t, os.WriteFile(usersPath, []byte(users), 0o600))
	assert.NoError(t, os.WriteFile(weatherPath, []byte(weather), 0o600))
	return []string{"SPECTRE_CANDIDATE_DATA=" + usersPath, "SPECTRE_CANDIDATE_WEATHER=" + weatherPath}
}

// rewrite returns a sample data file with each old and new pair replaced in
// turn, failing if any old text is missing.
func rewrite(t *testing.T, name string, oldNew ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "internal", "sample", "testdata", name))
	assert.NoError(t, err)
	text := string(data)
	for i := 0; i < len(oldNew); i += 2 {
		assert.Contains(t, text, oldNew[i])
		text = strings.Replace(text, oldNew[i], oldNew[i+1], 1)
	}
	return text
}

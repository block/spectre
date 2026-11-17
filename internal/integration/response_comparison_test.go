//go:build integration

package integration_test

import (
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/integration/it"
	"github.com/block/spectre/internal/model"
	samplepb "github.com/block/spectre/internal/sample/pb"
)

const (
	listUsersPath = "/spectre.sample.v1.UserService/ListUsers"
	listUsers     = model.Route("POST " + listUsersPath)
	getUser       = model.Route("POST /spectre.sample.v1.UserService/GetUser")
)

func TestNormalisedResponsesAreEquivalent(t *testing.T) {
	m := startSampleMirror(t)

	// The candidate reorders roles and generates its own timestamp, which the
	// sample's scripts normalise, so each call counts and none quarantines.
	listUserOne(t, m)
	m.ExpectCalls(it.Calls{listUsers: 1})
	listUserOne(t, m)
	m.ExpectCalls(it.Calls{listUsers: 2})
}

func TestRawHTTPResponsesAreEquivalent(t *testing.T) {
	m := startSampleMirror(t)

	// Raw HTTP responses are typed by the endpoints that weather.ts declares,
	// whose scripts treat the candidate's reordered alerts as equivalent.
	m.Ingress.Get("/v2/forecast?location=london").ExpectStatus(http.StatusOK).ExpectBodyContains(`"alerts"`)
	m.Ingress.Get("/api/v1/forecast?location=sydney").ExpectStatus(http.StatusOK)
	m.Ingress.Get("/_status").ExpectStatus(http.StatusOK)

	m.ExpectCalls(it.Calls{"GET /v2/forecast": 1, "GET /api/v1/forecast": 1, "GET /_status": 1})
}

func TestEgressReplaysReferenceResponses(t *testing.T) {
	m := startSampleMirror(t)

	m.Ingress.Get("/v2/forecast?location=london").ExpectStatus(http.StatusOK)
	m.Ingress.Get("/api/v1/forecast?location=sydney").ExpectStatus(http.StatusOK)

	m.ExpectCalls(it.Calls{"GET /v2/forecast": 1, "GET /api/v1/forecast": 1})
	// Egress answers the candidate with the reference's forecast, so each location
	// reaches the provider once.
	m.Dependency(forecastsHost).ExpectReceived("/v1/forecasts/london", 1)
	m.Dependency(forecastsHost).ExpectReceived("/v1/forecasts/sydney", 1)
}

func TestDivergentResponseQuarantinesCandidate(t *testing.T) {
	m := startSampleMirror(t)

	response, err := userClient(m).GetUser(t.Context(), &samplepb.GetUserRequest{Id: "user-2"})
	assert.NoError(t, err)
	assert.Equal(t, "Sam Sample", response.GetUser().GetName())
	assert.Equal(t, map[string]string{"team": "platform"}, response.GetUser().GetLabels())
	// Reasons never quote values, so the label's key is a wildcard.
	m.ExpectQuarantine(it.DivergentIngress(getUser, model.Difference{Path: "$.user.labels[*]", Change: model.ChangeChanged, Count: 1}))

	// The reference still answers. Ingress turns the candidate away before the
	// reference responds, so once ListUsers returns the candidate cannot get it.
	listUserOne(t, m)
	m.Candidate.ExpectReceived(listUsersPath, 0)
}

// startSampleMirror mirrors the sample under its own scripts, to a candidate
// that differs in ways the scripts normalise and in user-2's team.
func startSampleMirror(t *testing.T) *it.Mirror {
	t.Helper()
	return it.Start(t,
		it.ScriptsDir("../sample/scripts"),
		it.Reference(sampleService(revision("reference"))),
		it.Candidate(sampleService(revision("candidate"), reorderedRoles(), otherTeam(), swappedAlerts())),
		it.Dependency(forecastsHost, forecastProvider),
	)
}

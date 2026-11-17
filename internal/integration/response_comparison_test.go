//go:build integration

package integration_test

import (
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"

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
	listUserOne(m)
	m.expectCalls(calls{listUsers: 1})
	listUserOne(m)
	m.expectCalls(calls{listUsers: 2})
}

func TestRawHTTPResponsesAreEquivalent(t *testing.T) {
	m := startSampleMirror(t)

	// Raw HTTP responses are typed by the endpoints that weather.ts declares,
	// whose scripts treat the candidate's reordered alerts as equivalent.
	m.ingress.get("/v2/forecast?location=london").expectStatus(http.StatusOK).expectBodyContains(`"alerts"`)
	m.ingress.get("/api/v1/forecast?location=sydney").expectStatus(http.StatusOK)
	m.ingress.get("/_status").expectStatus(http.StatusOK)

	m.expectCalls(calls{"GET /v2/forecast": 1, "GET /api/v1/forecast": 1, "GET /_status": 1})
}

func TestEgressReplaysReferenceResponses(t *testing.T) {
	m := startSampleMirror(t)

	m.ingress.get("/v2/forecast?location=london").expectStatus(http.StatusOK)
	m.ingress.get("/api/v1/forecast?location=sydney").expectStatus(http.StatusOK)

	m.expectCalls(calls{"GET /v2/forecast": 1, "GET /api/v1/forecast": 1})
	// Egress answers the candidate with the reference's forecast, so each location
	// reaches the provider once.
	m.dependency(forecastsHost).expectReceived("/v1/forecasts/london", 1)
	m.dependency(forecastsHost).expectReceived("/v1/forecasts/sydney", 1)
}

func TestDivergentResponseQuarantinesCandidate(t *testing.T) {
	m := startSampleMirror(t)

	response, err := userClient(m).GetUser(t.Context(), &samplepb.GetUserRequest{Id: "user-2"})
	assert.NoError(t, err)
	assert.Equal(t, "Sam Sample", response.GetUser().GetName())
	assert.Equal(t, map[string]string{"team": "platform"}, response.GetUser().GetLabels())
	// Reasons never quote values, so the label's key is a wildcard.
	m.expectQuarantine(divergentIngress(getUser, model.Difference{Path: "$.user.labels[*]", Change: model.ChangeChanged, Count: 1}))

	// The reference still answers. Ingress turns the candidate away before the
	// reference responds, so once ListUsers returns the candidate cannot get it.
	listUserOne(m)
	m.candidate.expectReceived(listUsersPath, 0)
}

// startSampleMirror mirrors the sample under its own scripts, to a candidate
// that differs in ways the scripts normalise and in user-2's team.
func startSampleMirror(t *testing.T) *mirror {
	t.Helper()
	return startMirror(t,
		scriptsDir("../sample/scripts"),
		reference(sampleService(revision("reference"))),
		candidate(sampleService(revision("candidate"), reorderedRoles(), otherTeam(), swappedAlerts())),
		dependency(forecastsHost, forecastProvider),
	)
}

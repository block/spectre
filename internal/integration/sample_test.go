//go:build integration

package integration_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/sample"
	samplepb "github.com/block/spectre/internal/sample/pb"
	"github.com/block/spectre/internal/sample/pb/samplepbconnect"
)

// forecastsHost is the host the samples send their forecast requests to.
const forecastsHost = "forecasts.example"

// sampleData is what a sample service serves.
type sampleData struct {
	users        []byte
	weather      []byte
	revision     string
	ownForecasts bool
}

// variation changes what a sample service serves.
type variation func(t *testing.T, data *sampleData)

// sampleService serves the sample's users and weather, changed by variations.
// It fetches forecasts through egress from forecastsHost.
func sampleService(variations ...variation) service {
	return func(t *testing.T, backend backend) {
		t.Helper()
		data := sampleData{users: testdata(t, "users.json"), weather: testdata(t, "weather.json"), revision: "sample"}
		for _, vary := range variations {
			vary(t, &data)
		}
		users, err := sample.New(data.users)
		assert.NoError(t, err)
		forecasts := Some(sample.NewForecastClient(backend.egress.url, forecastsHost, backend.egress.transport))
		if data.ownForecasts {
			forecasts = None[*sample.ForecastClient]()
		}
		weather, err := sample.NewWeather(data.weather, data.revision, forecasts)
		assert.NoError(t, err)
		samplepbconnect.RegisterUserServiceHandler(backend.grpc, users)
		backend.http.HandleFunc("GET /api/v1/forecast", weather.GetForecast)
		backend.http.HandleFunc("GET /v2/forecast", weather.GetForecastV2)
		backend.http.HandleFunc("GET /_status", weather.GetStatus)
	}
}

// reorderedRoles swaps user-1's roles.
func reorderedRoles() variation {
	return func(t *testing.T, data *sampleData) {
		t.Helper()
		data.users = rewrite(t, data.users, `"ROLE_ADMIN", "ROLE_EDITOR"`, `"ROLE_EDITOR", "ROLE_ADMIN"`)
	}
}

// otherTeam moves user-2 to another team.
func otherTeam() variation {
	return func(t *testing.T, data *sampleData) {
		t.Helper()
		data.users = rewrite(t, data.users, `"team": "platform"`, `"team": "candidate"`)
	}
}

// swappedAlerts swaps the first and last weather alerts.
func swappedAlerts() variation {
	return func(t *testing.T, data *sampleData) {
		t.Helper()
		first := `"id": "alert-1", "severity": "moderate", "headline": "Heavy rain"`
		last := `"id": "alert-3", "severity": "minor", "headline": "Fog"`
		data.weather = rewrite(t, data.weather, first, "SWAPPED", last, first, "SWAPPED", last)
	}
}

// ownForecasts serves forecasts from the sample's data instead of fetching them.
func ownForecasts() variation {
	return func(_ *testing.T, data *sampleData) { data.ownForecasts = true }
}

// revision sets the revision the service reports on /_status.
func revision(name string) variation {
	return func(_ *testing.T, data *sampleData) { data.revision = name }
}

// forecastProvider serves the sample's forecasts, as the dependency at forecastsHost.
func forecastProvider(t *testing.T) http.Handler {
	t.Helper()
	provider, err := sample.NewForecastProvider(testdata(t, "weather.json"))
	assert.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/forecasts/{location}", provider.FetchForecast)
	return mux
}

// userClient returns a gRPC client of the sample's user service through ingress.
func userClient(m *mirror) samplepbconnect.UserServiceClient {
	return samplepbconnect.NewUserServiceClient(connect.NewClient(connecthttp.NewTransport(m.client, m.ingress.base, connecthttp.WithGRPC())))
}

// listUserOne lists user-1 through ingress, checking it gets the reference's answer.
func listUserOne(m *mirror) {
	m.t.Helper()
	response, err := userClient(m).ListUsers(m.t.Context(), &samplepb.ListUsersRequest{Ids: []string{"user-1"}})
	assert.NoError(m.t, err)
	names := []string{}
	for _, user := range response.GetUsers() {
		names = append(names, user.GetName())
	}
	assert.Equal(m.t, []string{"Alex Example"}, names)
}

// testdata reads one of the sample's data files.
func testdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "sample", "testdata", name))
	assert.NoError(t, err)
	return data
}

// rewrite returns data with each old and new pair replaced in turn, failing if
// any old text is missing.
func rewrite(t *testing.T, data []byte, oldNew ...string) []byte {
	t.Helper()
	text := string(data)
	for i := 0; i < len(oldNew); i += 2 {
		assert.Contains(t, text, oldNew[i])
		text = strings.Replace(text, oldNew[i], oldNew[i+1], 1)
	}
	return []byte(text)
}

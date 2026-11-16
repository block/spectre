package integration_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/grpcreflect/v2"
	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/integration"
	"github.com/block/spectre/internal/sample"
	samplepb "github.com/block/spectre/internal/sample/pb"
	"github.com/block/spectre/internal/sample/pb/samplepbconnect"
)

// forecastsHost is the host the samples send their forecast requests to.
const forecastsHost = "forecasts.example"

// startSampleMirror mirrors the sample to a candidate with reordered user-1
// roles, another user-2 team, and its first and last weather alerts swapped.
func startSampleMirror(t *testing.T) *integration.Running {
	t.Helper()
	users, weather := testdata(t, "users.json"), testdata(t, "weather.json")
	candidateUsers := rewrite(t, users,
		`"ROLE_ADMIN", "ROLE_EDITOR"`, `"ROLE_EDITOR", "ROLE_ADMIN"`,
		`"team": "platform"`, `"team": "candidate"`)
	first := `"id": "alert-1", "severity": "moderate", "headline": "Heavy rain"`
	last := `"id": "alert-3", "severity": "minor", "headline": "Fog"`
	candidateWeather := rewrite(t, weather, first, "SWAPPED", last, first, "SWAPPED", last)
	provider, err := sample.NewForecastProvider(weather)
	assert.NoError(t, err)
	forecasts := http.NewServeMux()
	forecasts.HandleFunc("GET /v1/forecasts/{location}", provider.FetchForecast)
	return integration.Start(t, integration.Mirror{
		Reference:      sampleService(t, "reference", users, weather),
		Candidate:      sampleService(t, "candidate", candidateUsers, candidateWeather),
		Dependencies:   map[string]http.Handler{forecastsHost: forecasts},
		ScriptsDir:     "../sample/scripts",
		SchemaDir:      "../sample/schema",
		DescriptorsDir: "../../dist/descriptors",
		ProxyFlags:     []string{"--match-window=1s"},
	})
}

// sampleService serves the sample's users and weather, fetching forecasts through egress.
func sampleService(t *testing.T, revision string, users []byte, weather []byte) integration.Service {
	t.Helper()
	return func(egress integration.Egress) http.Handler {
		service, err := sample.New(users)
		assert.NoError(t, err)
		forecasts := sample.NewForecastClient(egress.URL, forecastsHost, egress.Transport)
		weatherService, err := sample.NewWeather(weather, revision, Some(forecasts))
		assert.NoError(t, err)
		server := connect.NewServer()
		samplepbconnect.RegisterUserServiceHandler(server, service)
		grpcreflect.Register(server)
		mux := http.NewServeMux()
		connecthttp.Mount(mux, server)
		mux.HandleFunc("GET /api/v1/forecast", weatherService.GetForecast)
		mux.HandleFunc("GET /v2/forecast", weatherService.GetForecastV2)
		mux.HandleFunc("GET /_status", weatherService.GetStatus)
		return mux
	}
}

// users returns a gRPC client of the sample's user service through ingress.
func users(m *integration.Running) samplepbconnect.UserServiceClient {
	return samplepbconnect.NewUserServiceClient(connect.NewClient(connecthttp.NewTransport(m.Client(), m.IngressURL, connecthttp.WithGRPC())))
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

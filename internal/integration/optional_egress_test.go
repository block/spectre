//go:build integration

package integration_test

import (
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/grpcreflect/v2"
	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/sample"
)

const (
	// requiredHost serves the same forecasts as forecastsHost, on an endpoint that is not optional.
	requiredHost = "required.example"
	// ownForecastsWindow is the match window of the mirror whose candidate keeps its own forecasts.
	ownForecastsWindow = 250 * time.Millisecond
)

func TestCandidateMayLeaveOutOptionalEgressCalls(t *testing.T) {
	t.Parallel()
	m := startOwnForecastsMirror(t, "testdata/optional")

	// The reference fetches london's forecast, which the candidate reads from its own data.
	status, _ := m.get(t, m.ingressURL+"/v2/forecast?location=london")
	assert.Equal(t, http.StatusOK, status)
	expectCalls(t, m, map[model.Route]uint64{"GET /v2/forecast": 1})
	assert.Equal(t, 1, m.forecasts.count("/v1/forecasts/london"))

	// A required fetch the candidate leaves out quarantines it. Its window ends after
	// the optional fetch's, and only the first reason is kept, so the optional one never quarantined.
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, m.referenceEgressURL+"/v1/forecasts/london", nil)
	assert.NoError(t, err)
	request.Host = requiredHost
	status, _ = m.do(t, request)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, model.QuarantineReason{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: "GET required.example/v1/forecasts/{location}"}),
		Detail:  "no matching candidate request within 250ms",
	}, m.waitForQuarantine(t, model.Egress))
}

func TestCandidateLeavingOutEgressCallIsQuarantined(t *testing.T) {
	t.Parallel()
	// The sample's scripts declare the forecast endpoint without optional.
	m := startOwnForecastsMirror(t, "../sample/scripts")

	status, _ := m.get(t, m.ingressURL+"/v2/forecast?location=london")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, model.QuarantineReason{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: "GET forecasts.example/v1/forecasts/{location}"}),
		Detail:  "no matching candidate request within 250ms",
	}, m.waitForQuarantine(t, model.Egress))
	assert.Equal(t, 1, m.forecasts.count("/v1/forecasts/london"))
}

func TestUnmatchedCandidateCallToOptionalEndpointQuarantines(t *testing.T) {
	t.Parallel()
	m := startOwnForecastsMirror(t, "testdata/optional")

	// Optional only lets the candidate make fewer calls, not calls the reference never made.
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, m.candidateEgressURL+"/v1/forecasts/paris", nil)
	assert.NoError(t, err)
	request.Host = forecastsHost
	status, _ := m.do(t, request)
	assert.Equal(t, http.StatusBadGateway, status)
	assert.Equal(t, model.QuarantineReason{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: "GET forecasts.example/v1/forecasts/{location}"}),
		Detail:  "no matching reference request within 250ms",
	}, m.waitForQuarantine(t, model.Egress))
	assert.Equal(t, 0, m.forecasts.count("/v1/forecasts/paris"))
}

// startOwnForecastsMirror mirrors the weather sample, under the scripts in scriptsDir, to a
// candidate that serves forecasts from its own data instead of fetching them.
func startOwnForecastsMirror(t *testing.T, scriptsDir string) sampleMirror {
	t.Helper()
	weather := testdata(t, "weather.json")
	provider, err := sample.NewForecastProvider(weather)
	assert.NoError(t, err)
	forecasts := http.NewServeMux()
	forecasts.HandleFunc("GET /v1/forecasts/{location}", provider.FetchForecast)
	m := sampleMirror{candidate: newCalls(), forecasts: newCalls()}
	m.running = start(t, mirror{
		reference: func(egress egress) http.Handler {
			return weatherService(t, weather, Some(sample.NewForecastClient(egress.url, forecastsHost, egress.transport)))
		},
		candidate: func(egress) http.Handler {
			return m.candidate.wrap(weatherService(t, weather, None[*sample.ForecastClient]()))
		},
		dependencies:   map[string]http.Handler{forecastsHost: m.forecasts.wrap(forecasts), requiredHost: forecasts},
		scriptsDir:     scriptsDir,
		schemaDir:      "../sample/schema",
		descriptorsDir: "../../dist/descriptors",
		matchWindow:    ownForecastsWindow,
	})
	return m
}

// weatherService serves the sample's weather, fetching forecasts with forecasts if given,
// and gRPC reflection, which the proxy loads before it is ready.
func weatherService(t *testing.T, weather []byte, forecasts Option[*sample.ForecastClient]) http.Handler {
	t.Helper()
	service, err := sample.NewWeather(weather, "sample", forecasts)
	assert.NoError(t, err)
	server := connect.NewServer()
	grpcreflect.Register(server)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	mux.HandleFunc("GET /v2/forecast", service.GetForecastV2)
	return mux
}

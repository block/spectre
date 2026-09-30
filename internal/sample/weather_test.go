package sample_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/sample"
)

const testWeather = `{
  "london": {
    "forecast": {"location": "london", "days": [{"date": "2026-10-01", "precipitation_percent": 80}]},
    "alerts": [{"id": "alert-1", "headline": "Heavy rain"}]
  },
  "calm": {"forecast": {"location": "calm"}}
}`

func TestWeatherEndpoints(t *testing.T) {
	weather, err := sample.NewWeather([]byte(testWeather), "rev-1", nil)
	assert.NoError(t, err)
	london := `{"location": "london", "days": [{"date": "2026-10-01", "precipitation_percent": 80}]}`
	for name, test := range map[string]struct {
		handler  http.HandlerFunc
		query    string
		status   int
		expected string
	}{
		"V1Forecast":   {handler: weather.GetForecast, query: "location=london", status: http.StatusOK, expected: `{"forecast": ` + london + `}`},
		"V1BadRequest": {handler: weather.GetForecast, status: http.StatusBadRequest, expected: `{"success": false, "message": "Bad Request"}`},
		"V1NotFound":   {handler: weather.GetForecast, query: "location=mars", status: http.StatusNotFound, expected: `{"success": false, "message": "Not Found"}`},
		"V2WithAlerts": {handler: weather.GetForecastV2, query: "location=london", status: http.StatusOK, expected: `{"forecast": ` + london + `, "alerts": [{"id": "alert-1", "headline": "Heavy rain"}]}`},
		"V2NoAlerts":   {handler: weather.GetForecastV2, query: "location=calm", status: http.StatusOK, expected: `{"forecast": {"location": "calm"}}`},
		"V2BadRequest": {handler: weather.GetForecastV2, status: http.StatusBadRequest, expected: `{"success": false, "message": "Bad Request"}`},
		"Status":       {handler: weather.GetStatus, status: http.StatusOK, expected: `{"status": "ok", "revision": "rev-1"}`},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, httptest.NewRequest(http.MethodGet, "/?"+test.query, nil))
			assertJSONResponse(t, response, test.status, test.expected)
		})
	}
}

func TestWeatherFetchesForecastsFromProvider(t *testing.T) {
	provider, err := sample.NewForecastProvider([]byte(`{"london": {"forecast": {"location": "london", "days": [{"date": "remote"}]}}}`))
	assert.NoError(t, err)
	hosts := make(chan string, 10)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/forecasts/{location}", provider.FetchForecast)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hosts <- request.Host
		mux.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	weather := newRemoteWeather(t, server.URL)
	remote := `{"location": "london", "days": [{"date": "remote"}]}`
	for name, test := range map[string]struct {
		handler  http.HandlerFunc
		query    string
		status   int
		expected string
	}{
		"V1Forecast":    {handler: weather.GetForecast, query: "location=london", status: http.StatusOK, expected: `{"forecast": ` + remote + `}`},
		"V2LocalAlerts": {handler: weather.GetForecastV2, query: "location=london", status: http.StatusOK, expected: `{"forecast": ` + remote + `, "alerts": [{"id": "alert-1", "headline": "Heavy rain"}]}`},
		"V2NotFound":    {handler: weather.GetForecastV2, query: "location=calm", status: http.StatusNotFound, expected: `{"success": false, "message": "Not Found"}`},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, httptest.NewRequest(http.MethodGet, "/?"+test.query, nil))
			assertJSONResponse(t, response, test.status, test.expected)
			assert.Equal(t, "forecasts.example", <-hosts)
		})
	}
}

func TestWeatherReportsProviderFailureAsBadGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	weather := newRemoteWeather(t, server.URL)

	response := httptest.NewRecorder()
	weather.GetForecast(response, httptest.NewRequest(http.MethodGet, "/?location=london", nil))

	assertJSONResponse(t, response, http.StatusBadGateway, `{"success": false, "message": "Bad Gateway"}`)
}

func TestForecastProvider(t *testing.T) {
	provider, err := sample.NewForecastProvider([]byte(testWeather))
	assert.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/forecasts/{location}", provider.FetchForecast)
	for name, test := range map[string]struct {
		location string
		status   int
		expected string
	}{
		"Known":   {location: "london", status: http.StatusOK, expected: `{"forecast": {"location": "london", "days": [{"date": "2026-10-01", "precipitation_percent": 80}]}}`},
		"Unknown": {location: "mars", status: http.StatusNotFound, expected: `{"message": "Not Found"}`},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/forecasts/"+test.location, nil))
			assertJSONResponse(t, response, test.status, test.expected)
		})
	}
}

func newRemoteWeather(t *testing.T, provider string) *sample.Weather {
	t.Helper()
	target, err := url.Parse(provider)
	assert.NoError(t, err)
	client := sample.NewForecastClient(target, "forecasts.example", http.DefaultTransport, slog.New(slog.DiscardHandler))
	weather, err := sample.NewWeather([]byte(testWeather), "rev-1", client)
	assert.NoError(t, err)
	return weather
}

func assertJSONResponse(t *testing.T, response *httptest.ResponseRecorder, status int, expected string) {
	t.Helper()
	assert.Equal(t, status, response.Code)
	assert.Equal(t, "application/json; charset=utf-8", response.Header().Get("Content-Type"))
	var expectedJSON, actualJSON any
	assert.NoError(t, json.Unmarshal([]byte(expected), &expectedJSON))
	assert.NoError(t, json.Unmarshal(response.Body.Bytes(), &actualJSON))
	assert.Equal(t, expectedJSON, actualJSON)
}

func TestLoadsSampleWeather(t *testing.T) {
	data, err := os.ReadFile("testdata/weather.json")
	assert.NoError(t, err)
	_, err = sample.NewWeather(data, "", nil)
	assert.NoError(t, err)
}

func TestRejectsInvalidWeather(t *testing.T) {
	for name, data := range map[string]string{
		"NotAnObject":  `[]`,
		"UnknownField": `{"london": {"unknown": true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := sample.NewWeather([]byte(data), "", nil)
			assert.Error(t, err)
		})
	}
}

package sample_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	weather, err := sample.NewWeather([]byte(testWeather), "rev-1")
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
			assert.Equal(t, test.status, response.Code)
			assert.Equal(t, "application/json; charset=utf-8", response.Header().Get("Content-Type"))
			var expected, actual any
			assert.NoError(t, json.Unmarshal([]byte(test.expected), &expected))
			assert.NoError(t, json.Unmarshal(response.Body.Bytes(), &actual))
			assert.Equal(t, expected, actual)
		})
	}
}

func TestLoadsSampleWeather(t *testing.T) {
	data, err := os.ReadFile("testdata/weather.json")
	assert.NoError(t, err)
	_, err = sample.NewWeather(data, "")
	assert.NoError(t, err)
}

func TestRejectsInvalidWeather(t *testing.T) {
	for name, data := range map[string]string{
		"NotAnObject":  `[]`,
		"UnknownField": `{"london": {"unknown": true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := sample.NewWeather([]byte(data), "")
			assert.Error(t, err)
		})
	}
}

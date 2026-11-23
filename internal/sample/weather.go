package sample

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// Weather serves raw HTTP JSON endpoints modelled on a legacy weather service.
type Weather struct {
	// Locations are immutable after construction and keyed by location name.
	locations map[string]GetForecastV2Response
	revision  string
	// forecasts, when set, supplies forecasts in place of the local locations.
	// Alerts always come from the local locations.
	forecasts Option[*ForecastClient]
}

// NewWeather loads weather from a JSON object mapping locations to
// GetForecastV2Response values. A forecasts client enables dependency mode.
func NewWeather(data []byte, revision string, forecasts Option[*ForecastClient]) (*Weather, error) {
	locations, err := parseLocations(data)
	if err != nil {
		return nil, err
	}
	return &Weather{locations: locations, revision: revision, forecasts: forecasts}, nil
}

// GetForecast serves GET /api/v1/forecast for a location.
func (w *Weather) GetForecast(writer http.ResponseWriter, request *http.Request) {
	forecast, status := w.forecast(request.Context(), request.Header.Values("Baggage"), request.URL.Query().Get("location"))
	if status != http.StatusOK {
		writeWeatherError(writer, status)
		return
	}
	writeJSON(writer, http.StatusOK, &GetForecastResponse{Forecast: forecast})
}

// GetForecastV2 serves GET /v2/forecast for a location, including its alerts.
func (w *Weather) GetForecastV2(writer http.ResponseWriter, request *http.Request) {
	name := request.URL.Query().Get("location")
	forecast, status := w.forecast(request.Context(), request.Header.Values("Baggage"), name)
	if status != http.StatusOK {
		writeWeatherError(writer, status)
		return
	}
	var alerts []Alert
	if location, known := w.locations[name]; known {
		alerts = location.Alerts
	}
	writeJSON(writer, http.StatusOK, &GetForecastV2Response{
		Forecast: forecast,
		Alerts:   alerts,
	})
}

// GetStatus serves GET /_status.
func (w *Weather) GetStatus(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, &GetStatusResponse{Status: "ok", Revision: w.revision})
}

// forecast passes the request's baggage on to the provider, as services under SPECTRE must.
func (w *Weather) forecast(ctx context.Context, baggage []string, name string) (forecast Option[Forecast], status int) {
	if name == "" {
		return None[Forecast](), http.StatusBadRequest
	}
	if forecasts, ok := w.forecasts.Get(); ok {
		return forecasts.Fetch(ctx, baggage, name)
	}
	location, known := w.locations[name]
	if !known {
		return None[Forecast](), http.StatusNotFound
	}
	return location.Forecast, http.StatusOK
}

// parseLocations decodes a JSON object mapping locations to
// GetForecastV2Response values.
func parseLocations(data []byte) (map[string]GetForecastV2Response, error) {
	// GetForecastV2Response rejects unknown fields itself, because Option bypasses decoder settings.
	decoded := map[string]Option[GetForecastV2Response]{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, errors.Wrap(err, "decode sample weather")
	}
	locations := make(map[string]GetForecastV2Response, len(decoded))
	for name, entry := range decoded {
		location, ok := entry.Get()
		if !ok {
			return nil, errors.Errorf("decode sample weather for %q: expected an object", name)
		}
		locations[name] = location
	}
	return locations, nil
}

func writeWeatherError(writer http.ResponseWriter, status int) {
	writeJSON(writer, status, &GetForecastResponse{
		Success: Some(false),
		Message: Some(http.StatusText(status)),
	})
}

func writeJSON[T any](writer http.ResponseWriter, status int, message T) {
	body, err := json.Marshal(message)
	if err != nil {
		http.Error(writer, "encode response", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(body) //nolint:errcheck // Headers are sent, so a failed write cannot be reported.
}

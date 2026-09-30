package sample

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	samplepb "github.com/block/spectre/internal/sample/pb"
)

// Weather serves raw HTTP JSON endpoints modelled on a legacy weather service.
// WeatherService in weather.proto types its responses.
type Weather struct {
	// Locations are immutable after construction and keyed by location name.
	locations map[string]*samplepb.GetForecastV2Response
	revision  string
	// forecasts, when set, supplies forecasts in place of the local locations.
	// Alerts always come from the local locations.
	forecasts *ForecastClient
}

// NewWeather loads weather from a JSON object mapping locations to ProtoJSON
// GetForecastV2Response values. A non-nil forecasts client enables dependency mode.
func NewWeather(data []byte, revision string, forecasts *ForecastClient) (*Weather, error) {
	locations, err := parseLocations(data)
	if err != nil {
		return nil, err
	}
	return &Weather{locations: locations, revision: revision, forecasts: forecasts}, nil
}

// GetForecast serves GET /api/v1/forecast for a location.
func (w *Weather) GetForecast(writer http.ResponseWriter, request *http.Request) {
	forecast, status := w.forecast(request.Context(), request.URL.Query().Get("location"))
	if status != http.StatusOK {
		writeWeatherError(writer, status)
		return
	}
	writeJSON(writer, http.StatusOK, &samplepb.GetForecastResponse{Forecast: forecast})
}

// GetForecastV2 serves GET /v2/forecast for a location, including its alerts.
func (w *Weather) GetForecastV2(writer http.ResponseWriter, request *http.Request) {
	name := request.URL.Query().Get("location")
	forecast, status := w.forecast(request.Context(), name)
	if status != http.StatusOK {
		writeWeatherError(writer, status)
		return
	}
	writeJSON(writer, http.StatusOK, &samplepb.GetForecastV2Response{
		Forecast: forecast,
		Alerts:   w.locations[name].GetAlerts(),
	})
}

// GetStatus serves GET /_status.
func (w *Weather) GetStatus(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, &samplepb.GetStatusResponse{Status: "ok", Revision: w.revision})
}

func (w *Weather) forecast(ctx context.Context, name string) (forecast *samplepb.Forecast, status int) {
	if name == "" {
		return nil, http.StatusBadRequest
	}
	if w.forecasts != nil {
		return w.forecasts.Fetch(ctx, name)
	}
	location, known := w.locations[name]
	if !known {
		return nil, http.StatusNotFound
	}
	return location.GetForecast(), http.StatusOK
}

// parseLocations decodes a JSON object mapping locations to ProtoJSON
// GetForecastV2Response values.
func parseLocations(data []byte) (map[string]*samplepb.GetForecastV2Response, error) {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, errors.Wrap(err, "decode sample weather")
	}
	locations := make(map[string]*samplepb.GetForecastV2Response, len(raw))
	for name, message := range raw {
		location := &samplepb.GetForecastV2Response{}
		if err := protojson.Unmarshal(message, location); err != nil {
			return nil, errors.Wrapf(err, "decode sample weather for %q", name)
		}
		locations[name] = location
	}
	return locations, nil
}

func writeWeatherError(writer http.ResponseWriter, status int) {
	writeJSON(writer, status, &samplepb.GetForecastResponse{
		Success: new(false),
		Message: new(http.StatusText(status)),
	})
}

// writeJSON uses protobuf field names, as the legacy service renders snake_case keys.
func writeJSON(writer http.ResponseWriter, status int, message proto.Message) {
	body, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(message)
	if err != nil {
		http.Error(writer, "encode response", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(body) //nolint:errcheck // Headers are sent, so a failed write cannot be reported.
}

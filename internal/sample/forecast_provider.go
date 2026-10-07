package sample

import (
	"net/http"

	. "github.com/alecthomas/types/optional"
)

// ForecastProvider serves the raw HTTP JSON forecast API that the weather sample
// calls in dependency mode.
type ForecastProvider struct {
	// Forecasts are immutable after construction and keyed by location name.
	forecasts map[string]Option[Forecast]
}

// NewForecastProvider loads forecasts from the same data format as NewWeather,
// ignoring alerts.
func NewForecastProvider(data []byte) (*ForecastProvider, error) {
	locations, err := parseLocations(data)
	if err != nil {
		return nil, err
	}
	forecasts := make(map[string]Option[Forecast], len(locations))
	for name, location := range locations {
		forecasts[name] = location.Forecast
	}
	return &ForecastProvider{forecasts: forecasts}, nil
}

// FetchForecast serves GET /v1/forecasts/{location}.
func (p *ForecastProvider) FetchForecast(writer http.ResponseWriter, request *http.Request) {
	forecast, known := p.forecasts[request.PathValue("location")]
	if !known {
		writeJSON(writer, http.StatusNotFound, &FetchForecastResponse{Message: Some(http.StatusText(http.StatusNotFound))})
		return
	}
	writeJSON(writer, http.StatusOK, &FetchForecastResponse{Forecast: forecast})
}

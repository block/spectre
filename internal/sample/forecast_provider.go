package sample

import (
	"net/http"

	samplepb "github.com/block/spectre/internal/sample/pb"
)

// ForecastProvider serves the raw HTTP JSON forecast API that the weather sample
// calls in dependency mode. ForecastProviderService in forecasts.proto types it.
type ForecastProvider struct {
	// Forecasts are immutable after construction and keyed by location name.
	forecasts map[string]*samplepb.Forecast
}

// NewForecastProvider loads forecasts from the same data format as NewWeather,
// ignoring alerts.
func NewForecastProvider(data []byte) (*ForecastProvider, error) {
	locations, err := parseLocations(data)
	if err != nil {
		return nil, err
	}
	forecasts := make(map[string]*samplepb.Forecast, len(locations))
	for name, location := range locations {
		forecasts[name] = location.GetForecast()
	}
	return &ForecastProvider{forecasts: forecasts}, nil
}

// FetchForecast serves GET /v1/forecasts/{location}.
func (p *ForecastProvider) FetchForecast(writer http.ResponseWriter, request *http.Request) {
	forecast, known := p.forecasts[request.PathValue("location")]
	if !known {
		writeJSON(writer, http.StatusNotFound, &samplepb.FetchForecastResponse{Message: new(http.StatusText(http.StatusNotFound))})
		return
	}
	writeJSON(writer, http.StatusOK, &samplepb.FetchForecastResponse{Forecast: forecast})
}

package sample

// Forecast holds a location's daily forecasts.
type Forecast struct {
	Location string          `json:"location,omitempty"`
	Days     []DailyForecast `json:"days,omitempty"`
}

// DailyForecast holds the weather expected on one date.
type DailyForecast struct {
	Date                 string       `json:"date,omitempty"`
	High                 *Temperature `json:"high,omitempty"`
	Low                  *Temperature `json:"low,omitempty"`
	PrecipitationPercent int32        `json:"precipitation_percent,omitempty"`
	Wind                 *Wind        `json:"wind,omitempty"`
}

// Temperature holds a measurement and its unit.
type Temperature struct {
	Degrees float64 `json:"degrees,omitempty"`
	Unit    string  `json:"unit,omitempty"`
}

// Wind holds its speed and direction.
type Wind struct {
	SpeedKph  float64 `json:"speed_kph,omitempty"`
	Direction string  `json:"direction,omitempty"`
}

// Alert describes an active weather warning.
type Alert struct {
	ID       string `json:"id,omitempty"`
	Severity string `json:"severity,omitempty"`
	Headline string `json:"headline,omitempty"`
}

// GetForecastResponse holds a forecast or an error.
// Scalar pointers preserve explicit false and empty values without emitting absent fields.
type GetForecastResponse struct {
	Forecast *Forecast `json:"forecast,omitempty"`
	Success  *bool     `json:"success,omitempty"`
	Message  *string   `json:"message,omitempty"`
}

// GetForecastV2Response adds the location's active alerts, in no particular order.
type GetForecastV2Response struct {
	Forecast *Forecast `json:"forecast,omitempty"`
	Alerts   []Alert   `json:"alerts,omitempty"`
	Success  *bool     `json:"success,omitempty"`
	Message  *string   `json:"message,omitempty"`
}

// GetStatusResponse reports the service status and revision.
type GetStatusResponse struct {
	Status   string  `json:"status,omitempty"`
	Revision string  `json:"revision,omitempty"`
	Message  *string `json:"message,omitempty"`
}

// FetchForecastResponse holds a provider forecast or an error message.
type FetchForecastResponse struct {
	Forecast *Forecast `json:"forecast,omitempty"`
	Message  *string   `json:"message,omitempty"`
}

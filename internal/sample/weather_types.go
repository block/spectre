package sample

import (
	"bytes"
	"encoding/json"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// Forecast holds a location's daily forecasts.
type Forecast struct {
	Location string          `json:"location,omitempty"`
	Days     []DailyForecast `json:"days,omitempty"`
}

// UnmarshalJSON rejects unknown fields; see decodeStrict.
func (f *Forecast) UnmarshalJSON(data []byte) error {
	type plain Forecast
	return decodeStrict(data, (*plain)(f))
}

// DailyForecast holds the weather expected on one date.
type DailyForecast struct {
	Date                 string              `json:"date,omitempty"`
	High                 Option[Temperature] `json:"high,omitzero"`
	Low                  Option[Temperature] `json:"low,omitzero"`
	PrecipitationPercent int32               `json:"precipitation_percent,omitempty"`
	Wind                 Option[Wind]        `json:"wind,omitzero"`
}

// Temperature holds a measurement and its unit.
type Temperature struct {
	Degrees float64 `json:"degrees,omitempty"`
	Unit    string  `json:"unit,omitempty"`
}

// UnmarshalJSON rejects unknown fields; see decodeStrict.
func (t *Temperature) UnmarshalJSON(data []byte) error {
	type plain Temperature
	return decodeStrict(data, (*plain)(t))
}

// Wind holds its speed and direction.
type Wind struct {
	SpeedKph  float64 `json:"speed_kph,omitempty"`
	Direction string  `json:"direction,omitempty"`
}

// UnmarshalJSON rejects unknown fields; see decodeStrict.
func (w *Wind) UnmarshalJSON(data []byte) error {
	type plain Wind
	return decodeStrict(data, (*plain)(w))
}

// Alert describes an active weather warning.
type Alert struct {
	ID       string `json:"id,omitempty"`
	Severity string `json:"severity,omitempty"`
	Headline string `json:"headline,omitempty"`
}

// GetForecastResponse holds a forecast or an error.
type GetForecastResponse struct {
	Forecast Option[Forecast] `json:"forecast,omitzero"`
	Success  Option[bool]     `json:"success,omitzero"`
	Message  Option[string]   `json:"message,omitzero"`
}

// GetForecastV2Response adds the location's active alerts, in no particular order.
type GetForecastV2Response struct {
	Forecast Option[Forecast] `json:"forecast,omitzero"`
	Alerts   []Alert          `json:"alerts,omitempty"`
	Success  Option[bool]     `json:"success,omitzero"`
	Message  Option[string]   `json:"message,omitzero"`
}

// UnmarshalJSON rejects unknown fields; see decodeStrict.
func (r *GetForecastV2Response) UnmarshalJSON(data []byte) error {
	type plain GetForecastV2Response
	return decodeStrict(data, (*plain)(r))
}

// GetStatusResponse reports the service status and revision.
type GetStatusResponse struct {
	Status   string         `json:"status,omitempty"`
	Revision string         `json:"revision,omitempty"`
	Message  Option[string] `json:"message,omitzero"`
}

// FetchForecastResponse holds a provider forecast or an error message.
type FetchForecastResponse struct {
	Forecast Option[Forecast] `json:"forecast,omitzero"`
	Message  Option[string]   `json:"message,omitzero"`
}

// UnmarshalJSON rejects unknown fields; see decodeStrict.
func (r *FetchForecastResponse) UnmarshalJSON(data []byte) error {
	type plain FetchForecastResponse
	return decodeStrict(data, (*plain)(r))
}

// decodeStrict decodes one JSON value and rejects unknown fields. Option decodes
// with json.Unmarshal, so every type held in an Option must enforce this itself.
func decodeStrict[T any](data []byte, value *T) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return errors.WithStack(decoder.Decode(value))
}

package sample

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// maxForecastBytes bounds a provider response, which is small in practice.
const maxForecastBytes = 1 << 20

// ForecastClient fetches forecasts from a ForecastProvider, usually through egress.
// It sends the provider's host name so egress can select the destination.
type ForecastClient struct {
	target *url.URL
	host   string
	client *http.Client
	log    *slog.Logger
}

// NewForecastClient sends requests for host to target through transport.
func NewForecastClient(target *url.URL, host string, transport http.RoundTripper, log *slog.Logger) *ForecastClient {
	return &ForecastClient{
		target: target,
		host:   host,
		client: &http.Client{Transport: transport},
		log:    log,
	}
}

// Fetch returns the forecast for a location, or the status to report instead.
// A provider failure other than an unknown location is reported as a bad gateway.
func (c *ForecastClient) Fetch(ctx context.Context, location string) (forecast Option[Forecast], status int) {
	forecast, status, err := c.fetch(ctx, location)
	if err != nil {
		c.log.ErrorContext(ctx, "Forecast provider request failed", "location", location, "error", err)
		return None[Forecast](), http.StatusBadGateway
	}
	return forecast, status
}

func (c *ForecastClient) fetch(ctx context.Context, location string) (forecast Option[Forecast], status int, err error) {
	target := *c.target
	target.Path = "/v1/forecasts/" + location
	target.RawPath = "/v1/forecasts/" + url.PathEscape(location)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return None[Forecast](), 0, errors.Wrap(err, "build forecast request")
	}
	request.Host = c.host
	response, err := c.client.Do(request)
	if err != nil {
		return None[Forecast](), 0, errors.Wrap(err, "send forecast request")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return None[Forecast](), http.StatusNotFound, nil
	default:
		return None[Forecast](), 0, errors.Errorf("forecast provider returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxForecastBytes))
	if err != nil {
		return None[Forecast](), 0, errors.Wrap(err, "read forecast response")
	}
	// FetchForecastResponse rejects unknown fields itself, because Option bypasses decoder settings.
	var decoded Option[FetchForecastResponse]
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&decoded); err != nil {
		return None[Forecast](), 0, errors.Wrap(err, "decode forecast response")
	}
	provided, ok := decoded.Get()
	if !ok {
		return None[Forecast](), 0, errors.New("decode forecast response: expected an object")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return None[Forecast](), 0, errors.New("decode forecast response: unexpected trailing JSON value")
		}
		return None[Forecast](), 0, errors.Wrap(err, "decode forecast response")
	}
	return provided.Forecast, http.StatusOK, nil
}

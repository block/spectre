package ingress

import (
	"net/http"

	"github.com/alecthomas/errors"
)

// Transport forwards HTTP/1, encrypted HTTP/2, and unencrypted HTTP/2 requests.
type Transport struct {
	standard *http.Transport
	h2c      *http.Transport
}

// NewTransport constructs transports for HTTP/1, encrypted HTTP/2, and h2c.
func NewTransport(config Config) *Transport {
	standardProtocols := new(http.Protocols)
	standardProtocols.SetHTTP1(true)
	standardProtocols.SetHTTP2(true)
	standard := http.DefaultTransport.(*http.Transport).Clone()
	// Backend traffic must not escape the pod through ambient proxy settings.
	standard.Proxy = nil
	standard.Protocols = standardProtocols
	standard.DisableCompression = true
	standard.MaxConnsPerHost = config.MaxInFlightRequests
	standard.MaxResponseHeaderBytes = int64(config.MaxHeaderBytes)

	h2cProtocols := new(http.Protocols)
	h2cProtocols.SetUnencryptedHTTP2(true)
	h2c := http.DefaultTransport.(*http.Transport).Clone()
	h2c.Proxy = nil
	h2c.Protocols = h2cProtocols
	h2c.DisableCompression = true
	h2c.MaxConnsPerHost = config.MaxInFlightRequests
	h2c.MaxResponseHeaderBytes = int64(config.MaxHeaderBytes)
	return &Transport{standard: standard, h2c: h2c}
}

// RoundTrip sends HTTP requests without deriving backend protocol from the client.
func (t *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.standard.RoundTrip(request)
	return response, errors.Wrap(err, "forward HTTP request")
}

func (t *Transport) forBackend(h2c bool) http.RoundTripper {
	if h2c {
		return roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			response, err := t.h2c.RoundTrip(request)
			return response, errors.Wrap(err, "forward unencrypted HTTP/2 request")
		})
	}
	return t
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// CloseIdleConnections closes idle connections held by both underlying transports.
func (t *Transport) CloseIdleConnections() {
	t.standard.CloseIdleConnections()
	t.h2c.CloseIdleConnections()
}

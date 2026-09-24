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

// NewTransport constructs a transport that preserves the inbound HTTP protocol.
func NewTransport() *Transport {
	standardProtocols := new(http.Protocols)
	standardProtocols.SetHTTP1(true)
	standardProtocols.SetHTTP2(true)
	standard := http.DefaultTransport.(*http.Transport).Clone()
	standard.Protocols = standardProtocols

	h2cProtocols := new(http.Protocols)
	h2cProtocols.SetUnencryptedHTTP2(true)
	h2c := http.DefaultTransport.(*http.Transport).Clone()
	h2c.Protocols = h2cProtocols
	return &Transport{standard: standard, h2c: h2c}
}

// RoundTrip sends unencrypted HTTP/2 requests with prior knowledge of HTTP/2.
func (t *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme == "http" && request.ProtoMajor == 2 {
		response, err := t.h2c.RoundTrip(request)
		return response, errors.Wrap(err, "forward unencrypted HTTP/2 request")
	}
	response, err := t.standard.RoundTrip(request)
	return response, errors.Wrap(err, "forward HTTP request")
}

// CloseIdleConnections closes idle connections held by both underlying transports.
func (t *Transport) CloseIdleConnections() {
	t.standard.CloseIdleConnections()
	t.h2c.CloseIdleConnections()
}

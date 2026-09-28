package ingress

import (
	"net/http"
	"sync"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/netaddr"
)

// Transport forwards HTTP/1, encrypted HTTP/2, and unencrypted HTTP/2 requests.
type Transport struct {
	standard *http.Transport
	h2c      *http.Transport
	// mu guards unix, the per-backend transports that dial a fixed unix socket.
	mu   sync.Mutex
	unix []*http.Transport
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

// forBackend selects the transport for a backend, dialing its unix socket when
// the backend uses one.
func (t *Transport) forBackend(target *netaddr.Endpoint) http.RoundTripper {
	base := t.standard
	wrap := "forward HTTP request"
	if target.IsH2C() {
		base = t.h2c
		wrap = "forward unencrypted HTTP/2 request"
	}
	roundTripper := http.RoundTripper(base)
	if target.IsUnix() {
		roundTripper = t.unixTransport(base, target)
	}
	return roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response, err := roundTripper.RoundTrip(request)
		return response, errors.Wrap(err, wrap)
	})
}

// unixTransport clones base to dial the backend's unix socket and tracks it so
// idle connections close with the parent transport.
func (t *Transport) unixTransport(base *http.Transport, target *netaddr.Endpoint) *http.Transport {
	clone := base.Clone()
	clone.DialContext = target.DialContext
	t.mu.Lock()
	t.unix = append(t.unix, clone)
	t.mu.Unlock()
	return clone
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// CloseIdleConnections closes idle connections held by every underlying transport.
func (t *Transport) CloseIdleConnections() {
	t.standard.CloseIdleConnections()
	t.h2c.CloseIdleConnections()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, transport := range t.unix {
		transport.CloseIdleConnections()
	}
}

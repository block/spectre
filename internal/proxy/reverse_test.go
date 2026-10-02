package proxy_test

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/proxy"
)

// A unix-socket backend reaches many upstreams over one connection and routes by
// authority, so the reverse proxy forwards the inbound Host unchanged.
func TestReverseProxyPreservesHostForUnixBackend(t *testing.T) {
	gotHost := make(chan string, 1)
	// A short temp base keeps the socket path within the platform sun_path limit.
	t.Setenv("TMPDIR", "/tmp")
	socket := filepath.Join(t.TempDir(), "b.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	assert.NoError(t, err)
	backend := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		gotHost <- request.Host
	})}
	go func() { _ = backend.Serve(listener) }()
	t.Cleanup(func() { _ = backend.Close() })

	target, err := netaddr.ParseBackend("http+unix:" + socket)
	assert.NoError(t, err)
	reverse := proxy.NewReverseProxy(target, proxy.NewTransport(proxy.NewConfig()), slog.New(slog.DiscardHandler), false)

	request := httptest.NewRequest(http.MethodGet, "http://upstream.example/path", nil)
	reverse.ServeHTTP(httptest.NewRecorder(), request)

	select {
	case host := <-gotHost:
		assert.Equal(t, "upstream.example", host)
	case <-time.After(time.Second):
		t.Fatal("backend never received the forwarded request")
	}
}

// A TCP backend is fully addressed by its URL, so the forwarded Host is the
// target's host rather than the inbound one.
func TestReverseProxyUsesTargetHostForTCPBackend(t *testing.T) {
	gotHost := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		gotHost <- request.Host
	}))
	t.Cleanup(backend.Close)

	target, err := netaddr.ParseBackend(backend.URL)
	assert.NoError(t, err)
	reverse := proxy.NewReverseProxy(target, proxy.NewTransport(proxy.NewConfig()), slog.New(slog.DiscardHandler), false)

	request := httptest.NewRequest(http.MethodGet, "http://inbound.example/path", nil)
	reverse.ServeHTTP(httptest.NewRecorder(), request)

	select {
	case host := <-gotHost:
		assert.Equal(t, target.URL().Host, host)
	case <-time.After(time.Second):
		t.Fatal("backend never received the forwarded request")
	}
}

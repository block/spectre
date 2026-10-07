package netaddr_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/netaddr"
)

func TestParseBackendTCP(t *testing.T) {
	for _, test := range []struct {
		value  string
		scheme string
		host   string
		h2c    bool
		port   uint16
	}{
		{value: "http://example.test:8080", scheme: "http", host: "example.test:8080", port: 8080},
		{value: "https://example.test", scheme: "https", host: "example.test", port: 443},
		{value: "http://example.test", scheme: "http", host: "example.test", port: 80},
		{value: "h2c://127.0.0.1:9000", scheme: "http", host: "127.0.0.1:9000", h2c: true, port: 9000},
		{value: "http://example.test:8080/", scheme: "http", host: "example.test:8080", port: 8080},
	} {
		t.Run(test.value, func(t *testing.T) {
			backend, err := netaddr.ParseBackend(test.value)
			assert.NoError(t, err)
			assert.False(t, backend.IsUnix())
			assert.Equal(t, "", backend.Socket())
			assert.Equal(t, test.h2c, backend.IsH2C())
			assert.Equal(t, Some(&url.URL{Scheme: test.scheme, Host: test.host}), backend.URL())
			assert.Equal(t, test.port, backend.Port())
		})
	}
}

func TestParseBackendUnix(t *testing.T) {
	for _, test := range []struct {
		value  string
		socket string
		h2c    bool
	}{
		{value: "http+unix:/tmp/backend.sock", socket: "/tmp/backend.sock"},
		{value: "h2c+unix:/tmp/backend.sock", socket: "/tmp/backend.sock", h2c: true},
		{value: "h2c+unix:@abstract", socket: "@abstract", h2c: true},
	} {
		t.Run(test.value, func(t *testing.T) {
			backend, err := netaddr.ParseBackend(test.value)
			assert.NoError(t, err)
			assert.True(t, backend.IsUnix())
			assert.Equal(t, test.socket, backend.Socket())
			assert.Equal(t, test.h2c, backend.IsH2C())
			assert.Equal(t, Some(&url.URL{Scheme: "http", Host: "localhost"}), backend.URL())
		})
	}
}

func TestParseBackendRejectsInvalid(t *testing.T) {
	for _, value := range []string{
		"ftp://example.test",
		"example.test:8080",
		"http://",
		"http://example.test/path",
		"http://example.test?query",
		"http://example.test#fragment",
		"http://user@example.test",
		"http://example.test:0",
		"http://example.test:bogus",
		"http+unix:relative.sock",
		"https+unix:/tmp/backend.sock",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := netaddr.ParseBackend(value)
			assert.Error(t, err)
		})
	}
}

func TestParseBackendLoopback(t *testing.T) {
	loopback, err := netaddr.ParseBackend("http://127.0.0.1:8080")
	assert.NoError(t, err)
	assert.True(t, loopback.IsLoopback())

	remote, err := netaddr.ParseBackend("http://example.test:8080")
	assert.NoError(t, err)
	assert.False(t, remote.IsLoopback())

	socket, err := netaddr.ParseBackend("http+unix:/tmp/backend.sock")
	assert.NoError(t, err)
	assert.False(t, socket.IsLoopback())
}

func TestListenerHasNoDestination(t *testing.T) {
	listener := netaddr.ParseListen("127.0.0.1:8080")
	backend, err := netaddr.ParseBackend("http://127.0.0.1:8080")
	assert.NoError(t, err)
	assert.False(t, listener.SameDestination(listener))
	assert.False(t, backend.SameDestination(listener))
	assert.False(t, listener.TargetsListener(listener))
	assert.True(t, backend.TargetsListener(listener))
}

func TestParseListenLoopback(t *testing.T) {
	for address, loopback := range map[string]bool{
		"127.0.0.1:50050":  true,
		"[::1]:50050":      true,
		"0.0.0.0:50050":    false,
		":50050":           false,
		"localhost:50050":  false,
		"unix:/tmp/a.sock": false,
	} {
		assert.Equal(t, loopback, netaddr.ParseListen(address).IsLoopback(), address)
	}
}

func TestParseListen(t *testing.T) {
	tcp := netaddr.ParseListen("127.0.0.1:50050")
	assert.Equal(t, "tcp", tcp.Network())
	assert.Equal(t, "127.0.0.1:50050", tcp.Address())
	assert.False(t, tcp.IsUnix())
	assert.Equal(t, None[*url.URL](), tcp.URL())

	unix := netaddr.ParseListen("unix:/tmp/ingress.sock")
	assert.Equal(t, "unix", unix.Network())
	assert.Equal(t, "/tmp/ingress.sock", unix.Address())
	assert.True(t, unix.IsUnix())
}

func TestListenAndDialUnixSocket(t *testing.T) {
	socket := filepath.Join(shortSocketDir(t), "endpoint.sock")
	listener, err := netaddr.ParseListen("unix:" + socket).Listen(t.Context())
	assert.NoError(t, err)

	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { assert.NoError(t, server.Close()) })

	backend, err := netaddr.ParseBackend("http+unix:" + socket)
	assert.NoError(t, err)
	client := &http.Client{Transport: &http.Transport{DialContext: backend.DialContext}}
	response, err := client.Get("http://localhost/")
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, response.Body.Close()) })
	assert.Equal(t, http.StatusNoContent, response.StatusCode)
}

func TestListenRefusesNonSocketFile(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "regular")
	assert.NoError(t, os.WriteFile(path, []byte("not a socket"), 0o600))
	_, err := netaddr.ParseListen("unix:" + path).Listen(t.Context())
	assert.Error(t, err)
}

func TestListenTCP(t *testing.T) {
	listener, err := netaddr.ParseListen("127.0.0.1:0").Listen(t.Context())
	assert.NoError(t, err)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL)
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, response.Body.Close()) })
	assert.Equal(t, http.StatusNoContent, response.StatusCode)
}

// shortSocketDir returns a temporary directory with a short path so unix socket
// names stay within the operating system's limit.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	// A short base path keeps unix socket names within the 104-byte OS limit.
	dir, err := os.MkdirTemp("/tmp", "spectre") //nolint:usetesting // t.TempDir's base path is too long for unix sockets.
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(dir)) })
	return dir
}

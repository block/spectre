package proxies_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/httpserver"
	"github.com/block/spectre/internal/proxies"
	"github.com/block/spectre/internal/state"
)

func TestProxyMirrorsServicesThatRunElsewhere(t *testing.T) {
	reference := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("reference"))
	}))
	t.Cleanup(reference.Close)
	candidateCalls := make(chan struct{}, 1)
	candidate := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		candidateCalls <- struct{}{}
		_, _ = writer.Write([]byte("candidate"))
	}))
	t.Cleanup(candidate.Close)

	dir := shortSocketDir(t)
	socket := func(name string) string { return filepath.Join(dir, name) }
	config := proxies.ProxyConfig{
		Listen:                "unix:" + socket("ingress.sock"),
		ReferenceURL:          reference.URL,
		CandidateURL:          candidate.URL,
		ReferenceEgressListen: "unix:" + socket("egress-reference.sock"),
		CandidateEgressListen: "unix:" + socket("egress-candidate.sock"),
		EgressHealthListen:    "unix:" + socket("egress-health.sock"),
		HealthListen:          "unix:" + socket("health.sock"),
		Mirror:                proxies.NewConfig(),
	}
	config.Mirror.Reflection = false
	config.Mirror.ScriptsDir = t.TempDir()
	config.Mirror.SchemaDir = t.TempDir()
	proxy, err := proxies.NewProxy(t.Context(), config, testCatalog(t))
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan error, 1)
	assert.NoError(t, proxy.Start(ctx))
	go func() { stopped <- proxy.Wait(ctx) }()

	health := unixClient(socket("health.sock"))
	assert.Equal(t, http.StatusNoContent, status(t, health, "/livez"))
	deadline := time.Now().Add(5 * time.Second)
	for status(t, health, "/readyz") != http.StatusNoContent {
		assert.True(t, time.Now().Before(deadline), "proxies did not become ready")
		time.Sleep(10 * time.Millisecond)
	}

	response, err := unixClient(socket("ingress.sock")).Get("http://localhost/v1/items")
	assert.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	assert.NoError(t, err)
	assert.NoError(t, response.Body.Close())
	assert.Equal(t, "reference", string(body))
	select {
	case <-candidateCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("candidate received no mirrored request")
	}

	cancel()
	assert.NoError(t, <-stopped)
}

func TestNewProxyValidates(t *testing.T) {
	config := proxies.ProxyConfig{Mirror: proxies.NewConfig()}
	config.Mirror.MatchWindow = 0
	_, err := proxies.NewProxy(t.Context(), config, testCatalog(t))
	assert.EqualError(t, err, "match window must be positive")
}

// status returns the status code of a GET for path.
func status(t *testing.T, client *http.Client, path string) int {
	t.Helper()
	response, err := client.Get("http://localhost" + path)
	if err != nil {
		return 0
	}
	assert.NoError(t, response.Body.Close())
	return response.StatusCode
}

// unixClient sends every request to the unix socket at path.
func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", path)
	}}}
}

func testCatalog(t *testing.T) *state.Catalog {
	t.Helper()
	catalog, err := state.NewCatalog(httpserver.Supports()...)
	assert.NoError(t, err)
	return catalog
}

// shortSocketDir returns a temporary directory with a path short enough for unix
// socket names.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "spectre") //nolint:usetesting // t.TempDir's base path is too long for unix sockets.
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(dir)) })
	return dir
}

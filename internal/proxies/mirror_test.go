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

	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocols"
	httpprotocol "github.com/block/spectre/internal/protocols/http"
	mysqlprotocol "github.com/block/spectre/internal/protocols/mysql"
	"github.com/block/spectre/internal/protocols/protocolstest"
	"github.com/block/spectre/internal/proxies"
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
	config := proxies.NewConfig()
	config.EgressHealthListen = "unix:" + socket("egress-health.sock")
	config.HealthListen = "unix:" + socket("health.sock")
	config.ScriptsDir = t.TempDir()
	config.SchemaDir = t.TempDir()
	config.Statsd.ReferenceListen = "unix:" + socket("statsd-reference.sock")
	config.Statsd.CandidateListen = "unix:" + socket("statsd-candidate.sock")
	config.Statsd.Forward = "unix:" + socket("agent.sock")
	agent, err := netaddr.ParsePacket(config.Statsd.Forward).Listen(t.Context())
	assert.NoError(t, err)
	t.Cleanup(func() { _ = agent.Close() })
	registry := protocols.NewRegistry()
	assert.NoError(t, registry.Register(httpprotocol.NewProvider()))
	protocolstest.Configure(t, registry.Plugins(),
		"--listen=unix:"+socket("ingress.sock"),
		"--reference-url="+reference.URL,
		"--candidate-url="+candidate.URL,
		"--reference-egress-listen=unix:"+socket("egress-reference.sock"),
		"--candidate-egress-listen=unix:"+socket("egress-candidate.sock"),
		"--no-reflection",
	)
	proxy, err := proxies.New(t.Context(), config, registry)
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

	sendDatagram(t, socket("statsd-candidate.sock"), "candidate:1|c")
	sendDatagram(t, socket("statsd-reference.sock"), "reference:1|c")
	assert.NoError(t, agent.SetReadDeadline(time.Now().Add(5*time.Second)))
	buffer := make([]byte, 1024)
	size, _, err := agent.ReadFrom(buffer)
	assert.NoError(t, err)
	assert.Equal(t, "reference:1|c", string(buffer[:size]))

	cancel()
	assert.NoError(t, <-stopped)
}

// Egress is ready once every protocol that serves egress does, whether or not MySQL is proxied.
func TestEgressHealthIncludesMySQL(t *testing.T) {
	for name, upstream := range map[string]string{"Disabled": "", "Enabled": "127.0.0.1:1"} {
		t.Run(name, func(t *testing.T) {
			dir := shortSocketDir(t)
			socket := func(name string) string { return filepath.Join(dir, name) }
			var args []string
			if upstream != "" {
				args = []string{
					"--mysql-upstream=" + upstream,
					"--mysql-upstream-user=spectre",
					"--mysql-upstream-password-file=" + filepath.Join(dir, "password"),
					"--mysql-reference-listen=unix:" + socket("mysql-reference.sock"),
					"--mysql-candidate-listen=unix:" + socket("mysql-candidate.sock"),
				}
			}
			config := proxies.NewConfig()
			config.EgressHealthListen = "unix:" + socket("egress-health.sock")
			config.HealthListen = "unix:" + socket("health.sock")
			config.ScriptsDir = t.TempDir()
			config.SchemaDir = t.TempDir()
			registry := protocols.NewRegistry()
			assert.NoError(t, registry.Register(mysqlprotocol.NewProvider()))
			protocolstest.Configure(t, registry.Plugins(), args...)
			proxy, err := proxies.New(t.Context(), config, registry)
			assert.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			stopped := make(chan error, 1)
			assert.NoError(t, proxy.Start(ctx))
			go func() { stopped <- proxy.Wait(ctx) }()

			egress := unixClient(socket("egress-health.sock"))
			deadline := time.Now().Add(5 * time.Second)
			for status(t, egress, "/readyz") != http.StatusNoContent {
				assert.True(t, time.Now().Before(deadline), "egress did not become ready")
				time.Sleep(10 * time.Millisecond)
			}

			cancel()
			assert.NoError(t, <-stopped)
		})
	}
}

func TestNewProxyValidates(t *testing.T) {
	config := proxies.NewConfig()
	config.MatchWindow = 0
	_, err := proxies.New(t.Context(), config, nil)
	assert.EqualError(t, err, "match window must be positive")
}

// sendDatagram writes datagram to the unix datagram socket at path.
func sendDatagram(t *testing.T, path string, datagram string) {
	t.Helper()
	conn, err := netaddr.ParsePacket("unix:" + path).Dial(t.Context())
	assert.NoError(t, err)
	_, err = conn.Write([]byte(datagram))
	assert.NoError(t, err)
	assert.NoError(t, conn.Close())
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

// shortSocketDir returns a temporary directory with a path short enough for unix
// socket names.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "spectre") //nolint:usetesting // t.TempDir's base path is too long for unix sockets.
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(dir)) })
	return dir
}

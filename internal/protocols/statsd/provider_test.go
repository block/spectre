package statsd_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/statsd"
	"github.com/block/spectre/internal/proxy"
)

func TestProviderListensOnTheConfiguredDatagramSockets(t *testing.T) {
	forwarded := statsd.NewConfig()
	forwarded.ReferenceListen, forwarded.CandidateListen, forwarded.Forward = "unix:@reference.sock", "unix:@candidate.sock", "unix:@veneur.sock"
	candidateOnly := statsd.NewConfig()
	candidateOnly.CandidateListen = "127.0.0.1:8125"
	for name, test := range map[string]struct {
		config   statsd.Config
		expected []protocols.Listen
	}{
		"Disabled": {config: statsd.NewConfig(), expected: []protocols.Listen{}},
		"CandidateOnly": {config: candidateOnly, expected: []protocols.Listen{
			{Name: "candidate", Address: "127.0.0.1:8125", Transport: protocols.Datagram},
		}},
		"Forwarded": {config: forwarded, expected: []protocols.Listen{
			{Name: "reference", Address: "unix:@reference.sock", Transport: protocols.Datagram},
			{Name: "candidate", Address: "unix:@candidate.sock", Transport: protocols.Datagram},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.expected, statsd.NewProvider().Listens(test.config))
		})
	}
}

// Without listeners the server has nothing to relay, so it serves until the mirror stops, and is ready.
func TestServeWithoutListenersStopsWithTheMirror(t *testing.T) {
	provider := statsd.NewProvider()
	runtime, _ := egressRuntime(t, provider, "")
	server, err := provider.New(t.Context(), statsd.NewConfig(), runtime)
	assert.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, protocols.Listeners{}) }()
	side, _ := runtime.Egress.Get()
	awaitReady(t, side.Health)

	cancel()
	select {
	case err := <-served:
		assert.NoError(t, err)
	case <-time.After(receiveTimeout):
		t.Fatal("server did not stop")
	}
}

func TestServeForwardsTheReferenceAndDiscardsTheCandidate(t *testing.T) {
	dir := shortSocketDir(t)
	agent := listen(t, dir+"/agent.sock")
	provider := statsd.NewProvider()
	runtime, _ := egressRuntime(t, provider, "")
	server, err := provider.New(t.Context(), relayConfig(dir), runtime)
	assert.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	listeners := relayListeners(t, dir)
	go func() { served <- server.Serve(ctx, listeners) }()

	send(t, dir+"/candidate.sock", "candidate:1|c")
	send(t, dir+"/reference.sock", "reference:1|c")
	assert.Equal(t, "reference:1|c", read(t, agent))

	cancel()
	assert.NoError(t, <-served)
}

// With StatsD rules, a series the reference sends and the candidate leaves out quarantines the candidate.
func TestServeQuarantinesACandidateThatLeavesOutAComparedMetric(t *testing.T) {
	dir := shortSocketDir(t)
	agent := listen(t, dir+"/agent.sock")
	provider := statsd.NewProvider()
	runtime, candidates := egressRuntime(t, provider, `
		egress.statsd("framework.actions.**").map((m) => ({ name: "framework.http.response_codes" }));
		egress.statsd("framework.http.response_codes").compare({ total: { upper: 0.1, lower: 0.1 } });
		egress.statsd("jvm.**").ignore();
	`)
	config := relayConfig(dir)
	config.Window, config.Settle, config.CandidateStartupTimeout = 200*time.Millisecond, 50*time.Millisecond, 0
	server, err := provider.New(t.Context(), config, runtime)
	assert.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	listeners := relayListeners(t, dir)
	go func() { served <- server.Serve(ctx, listeners) }()

	send(t, dir+"/candidate.sock", "jvm.threads:20|g")
	send(t, dir+"/reference.sock", "framework.http.response_codes:5|c")
	assert.Equal(t, "framework.http.response_codes:5|c", read(t, agent))

	select {
	case quarantine := <-candidates.Quarantined():
		assert.Equal(t, model.QuarantineUnmatched, quarantine.Reason.Kind)
		assert.Equal(t, Some(model.Traffic{Direction: model.Egress, Protocol: "statsd", Route: "framework.http.response_codes"}), quarantine.Reason.Traffic)
		assert.Contains(t, quarantine.Reason.Detail, "candidate left out counter framework.http.response_codes that the reference sent: total below the 10% lower bound")
	case <-time.After(receiveTimeout):
		t.Fatal("candidate was not quarantined")
	}
	cancel()
	assert.NoError(t, <-served)
}

// relayConfig configures the reference, candidate and agent sockets in dir.
func relayConfig(dir string) statsd.Config {
	config := statsd.NewConfig()
	config.ReferenceListen, config.CandidateListen, config.Forward = "unix:"+dir+"/reference.sock", "unix:"+dir+"/candidate.sock", "unix:"+dir+"/agent.sock"
	return config
}

// relayListeners opens the reference and candidate sockets in dir.
func relayListeners(t *testing.T, dir string) protocols.Listeners {
	t.Helper()
	return protocols.Listeners{Datagrams: map[string]net.PacketConn{
		statsd.ReferenceListener: listen(t, dir+"/reference.sock"),
		statsd.CandidateListener: listen(t, dir+"/candidate.sock"),
	}}
}

// egressRuntime loads script with egress imported, and returns the egress side of a mirror with it.
func egressRuntime(t *testing.T, provider *statsd.Provider, script string) (protocols.Runtime, *proxy.Candidates) {
	t.Helper()
	config := comparison.NewConfig()
	config.ScriptsDir, config.Schema.SchemaDir = t.TempDir(), t.TempDir()
	source := `import { egress } from "spectre"; void egress;` + script
	assert.NoError(t, os.WriteFile(filepath.Join(config.ScriptsDir, "statsd.ts"), []byte(source), 0o600))
	loaded, err := comparison.New(t.Context(), config, []javascript.Binding{provider.Scripts()})
	assert.NoError(t, err)
	candidates := proxy.NewCandidates(proxy.NewCandidatesConfig())
	side := protocols.Side{Candidates: candidates, Quarantiner: candidates, Quarantines: candidates, Health: health.New(http.NotFoundHandler())}
	return protocols.Runtime{Schema: loaded.Schema(), Scripts: loaded, Egress: Some(side)}, candidates
}

// awaitReady waits until handler is ready.
func awaitReady(t *testing.T, handler *health.Handler) {
	t.Helper()
	deadline := time.Now().Add(receiveTimeout)
	for wait := time.Millisecond; !handler.Ready(); wait = min(2*wait, 50*time.Millisecond) {
		assert.True(t, time.Now().Before(deadline), "not ready")
		<-time.After(wait)
	}
}

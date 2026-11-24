package proxies

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

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/comparison/javascript/javascripttest"
	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/state"
)

func testMirror(t *testing.T, config Config) *Mirror {
	t.Helper()
	m, err := New(t.Context(), config, fakeRegistry(t, newFakeProvider()))
	assert.NoError(t, err)
	return m
}

func TestLoadsMigrationState(t *testing.T) {
	for name, test := range map[string]struct {
		state    string
		expected Option[[]state.Node]
		err      string
	}{
		"NoStateFile": {expected: None[[]state.Node]()},
		"Valid": {
			state:    "[nodes.money]\nstatus = \"migrated\"\n",
			expected: Some([]state.Node{{ID: "money", Status: state.Migrated, Line: 1}}),
		},
		"Invalid": {
			state: "[nodes.money]\nstatus = \"done\"\n",
			err:   `line 2: nodes.money.status: must be "pending" or "migrated"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := NewConfig()
			if test.state != "" {
				config.StateFile = filepath.Join(t.TempDir(), "state.toml")
				assert.NoError(t, os.WriteFile(config.StateFile, []byte(test.state), 0o600))
			}
			m, err := New(t.Context(), config, fakeRegistry(t, newFakeProvider()))
			if test.err != "" {
				assert.EqualError(t, err, "load migration state "+config.StateFile+": "+test.err)
				return
			}
			assert.NoError(t, err)
			actual := None[[]state.Node]()
			if migration, ok := m.migration.Get(); ok {
				actual = Some(migration.All)
			}
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestEngineConfig(t *testing.T) {
	config := NewConfig()
	config.ScriptsDir = "scripts"
	config.SchemaDir = "schema"
	config.MatchWindow = 3 * time.Second
	config.ScriptVariables = map[string]string{"environment": "staging"}
	engine := testMirror(t, config).engineConfig()
	assert.Equal(t, "scripts", engine.Comparison.ScriptsDir)
	assert.Equal(t, "schema", engine.Comparison.Schema.SchemaDir)
	assert.Equal(t, 3*time.Second, engine.MatchWindow)
	assert.Equal(t, map[string]string{"environment": "staging"}, engine.Comparison.Variables)
}

// loopbackConfig is a mirror configuration whose health endpoints take free ports.
func loopbackConfig(t *testing.T) Config {
	t.Helper()
	config := NewConfig()
	config.ScriptsDir = t.TempDir()
	config.SchemaDir = t.TempDir()
	config.EgressHealthListen = "127.0.0.1:0"
	config.HealthListen = "127.0.0.1:0"
	return config
}

// loopback opens a listener on a free loopback port, closed when the test ends.
func loopback(t *testing.T) net.Listener {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func statusOf(t *testing.T, listener net.Listener, path string) int {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String()+path, nil)
	assert.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	assert.NoError(t, err)
	assert.NoError(t, response.Body.Close())
	return response.StatusCode
}

// A bad address fails startup before any provider builds or serves anything.
func TestStartFailsOnAnUnusableListenerBeforeProvidersStart(t *testing.T) {
	occupied := loopback(t)
	provider := newFakeProvider(occupied.Addr().String())
	m, err := New(t.Context(), loopbackConfig(t), fakeRegistry(t, provider))
	assert.NoError(t, err)

	err = m.Start(t.Context())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "listen on "+occupied.Addr().String())
	assert.Equal(t, 0, len(provider.built))
}

func TestStartListenersServesEachProviderOnItsListenersAndCombinesHealth(t *testing.T) {
	provider := newFakeProvider("unused")
	m, err := New(t.Context(), loopbackConfig(t), fakeRegistry(t, provider))
	assert.NoError(t, err)
	main, healthListener, egressHealth := loopback(t), loopback(t), loopback(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	assert.NoError(t, m.StartListeners(ctx, Listeners{
		Providers:    map[model.Protocol]protocols.Listeners{javascripttest.Protocol: {Streams: map[string]net.Listener{"main": main}}},
		EgressHealth: egressHealth,
		Health:       healthListener,
	}))
	stopped := make(chan error, 1)
	go func() { stopped <- m.Wait(ctx) }()

	assert.Equal(t, protocols.Listeners{Streams: map[string]net.Listener{"main": main}}, <-provider.served)
	runtime := <-provider.built
	_, hasIngress := runtime.Ingress.Get()
	_, hasEgress := runtime.Egress.Get()
	assert.True(t, hasIngress && hasEgress)
	deadline := time.Now().Add(5 * time.Second)
	for statusOf(t, egressHealth, "/readyz") != http.StatusNoContent {
		assert.True(t, time.Now().Before(deadline), "egress did not become ready")
		time.Sleep(10 * time.Millisecond)
	}
	// Ingress never became ready, so the combined health stays unready.
	assert.Equal(t, http.StatusNoContent, statusOf(t, healthListener, "/livez"))
	assert.Equal(t, http.StatusServiceUnavailable, statusOf(t, healthListener, "/readyz"))
	ingress, _ := runtime.Ingress.Get()
	ingress.Health.SetReady(true)
	assert.Equal(t, http.StatusNoContent, statusOf(t, healthListener, "/readyz"))

	cancel()
	assert.NoError(t, <-stopped)
}

func TestStartListenersFailsWhenAProviderListenerIsNotOpen(t *testing.T) {
	m, err := New(t.Context(), loopbackConfig(t), fakeRegistry(t, newFakeProvider("unused")))
	assert.NoError(t, err)
	err = m.StartListeners(t.Context(), Listeners{Providers: map[model.Protocol]protocols.Listeners{}, EgressHealth: loopback(t), Health: loopback(t)})
	assert.EqualError(t, err, "fake stream listener main is not open")
}

// Candidate work and readiness stop before the servers are cancelled, so waiting
// candidate requests never hold up the servers' shutdown.
func TestStopMarksUnreadyAndClosesCandidatesBeforeCancellingServers(t *testing.T) {
	config := NewConfig()
	config.ScriptsDir = t.TempDir()
	config.SchemaDir = t.TempDir()
	m := testMirror(t, config)
	bindings := []javascript.Binding{javascripttest.Binding()}
	built, err := newShared(t.Context(), m.engineConfig(), m.config.Candidates, bindings, m.migration)
	assert.NoError(t, err)
	m.shared = Some(built)
	handler := health.New(http.NotFoundHandler())
	handler.SetReady(true)
	m.ready = []*health.Handler{handler}
	type observed struct{ ready, ingressAdmitted, egressAdmitted bool }
	seen := make(chan observed, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx, m.cancel = context.WithCancel(ctx)
	m.serve(ctx, "test", func() error {
		<-ctx.Done()
		_, ingressAdmitted := built.IngressCandidates.Start(t.Context(), func() {}).Get()
		_, egressAdmitted := built.EgressCandidates.Start(t.Context(), func() {}).Get()
		seen <- observed{ready: handler.Ready(), ingressAdmitted: ingressAdmitted, egressAdmitted: egressAdmitted}
		return nil
	})

	m.stop(t.Context())

	assert.Equal(t, observed{}, <-seen)
	assert.Equal(t, 0, m.running)
}

package proxies

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/httpserver"
	"github.com/block/spectre/internal/state"
)

// testProxyConfig wires the proxies to sockets, with mirror as their configuration.
func testProxyConfig(mirror Config) ProxyConfig {
	return ProxyConfig{
		Listen:                "127.0.0.1:50050",
		ReferenceURL:          "h2c+unix:/sockets/reference.sock",
		CandidateURL:          "http+unix:/sockets/candidate.sock",
		ReferenceEgressListen: "unix:/sockets/egress-reference.sock",
		CandidateEgressListen: "unix:/sockets/egress-candidate.sock",
		EgressHealthListen:    "unix:/sockets/egress-health.sock",
		HealthListen:          "unix:/sockets/health.sock",
		Mirror:                mirror,
	}
}

func testMirror(t *testing.T, config Config) *Mirror {
	t.Helper()
	m, err := New(t.Context(), testProxyConfig(config))
	assert.NoError(t, err)
	return m
}

func TestLoadsMigrationState(t *testing.T) {
	for name, test := range map[string]struct {
		state    string
		expected Option[state.State]
		err      string
	}{
		"NoStateFile": {expected: None[state.State]()},
		"Valid": {
			state:    "[nodes.money]\nstatus = \"migrated\"\n",
			expected: Some(state.State{Nodes: []state.Node{{ID: "money", Status: state.Migrated, Line: 1}}}),
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
			m, err := New(t.Context(), testProxyConfig(config))
			if test.err != "" {
				assert.EqualError(t, err, "load migration state "+config.StateFile+": "+test.err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.expected, m.migration)
		})
	}
}

func TestEgressConfig(t *testing.T) {
	config := NewConfig()
	config.Destinations = map[string]string{"b.example": "http://b", "a.example": "http://a"}
	config.Descriptors.DescriptorsDir = "descriptors"
	expected := httpserver.NewEgressConfig()
	expected.ReferenceListen = "unix:/sockets/egress-reference.sock"
	expected.CandidateListen = "unix:/sockets/egress-candidate.sock"
	expected.HealthListen = "unix:/sockets/egress-health.sock"
	expected.Destinations = config.Destinations
	expected.Descriptors = config.Descriptors
	assert.Equal(t, expected, testMirror(t, config).egressConfig())
}

func TestIngressConfig(t *testing.T) {
	config := NewConfig()
	config.Reflection = false
	config.CandidateStartupTimeout = time.Minute
	config.Descriptors.DescriptorsDir = "descriptors"
	expected := httpserver.NewIngressConfig()
	expected.Listen = "127.0.0.1:50050"
	expected.Reference = "h2c+unix:/sockets/reference.sock"
	expected.Candidate = "http+unix:/sockets/candidate.sock"
	expected.Reflection = false
	expected.CandidateStartupTimeout = time.Minute
	expected.Descriptors = config.Descriptors
	assert.Equal(t, expected, testMirror(t, config).ingressConfig())
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

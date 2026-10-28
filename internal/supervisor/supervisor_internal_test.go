package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/httpserver"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/state"
)

func testSupervisor(t *testing.T) (*Supervisor, Config) {
	t.Helper()
	config := testConfig(t)
	s, err := New(t.Context(), config, testCatalog(t))
	assert.NoError(t, err)
	return s, config
}

func testCatalog(t *testing.T) *state.Catalog {
	t.Helper()
	catalog, err := state.NewCatalog(httpserver.Supports()...)
	assert.NoError(t, err)
	return catalog
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
			expected: Some(state.State{Nodes: []state.Node{{ID: "money", Status: state.Migrated, IO: map[model.IOKind]map[string]any{}, Line: 1}}}),
		},
		"Invalid": {
			state: "[nodes.money]\nstatus = \"done\"\n",
			err:   `line 2: nodes.money.status: must be "pending" or "migrated"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := testConfig(t)
			if test.state != "" {
				config.StateFile = filepath.Join(t.TempDir(), "state.toml")
				assert.NoError(t, os.WriteFile(config.StateFile, []byte(test.state), 0o600))
			}
			s, err := New(t.Context(), config, testCatalog(t))
			if test.err != "" {
				assert.EqualError(t, err, "load migration state "+config.StateFile+": "+test.err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.expected, s.migration)
		})
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	config := NewConfig()
	config.Candidate = "run-candidate --port 1"
	config.Reference = "run-reference --port 2"
	config.SocketDir = t.TempDir()
	config.Destinations = map[string]string{"b.example": "http://b", "a.example": "http://a"}
	return config
}

func TestServiceProcessReference(t *testing.T) {
	s, _ := testSupervisor(t)
	process := s.serviceProcess(t.Context(), roleReference, "http+unix:/sockets/egress-reference.sock", "unix:/sockets/reference.sock", false)
	assert.Equal(t, []string{"/bin/sh", "-c", "run-reference --port 2"}, process.command().Args)
	assert.True(t, slices.Contains(process.command().Env, "SPECTRE_LISTEN=unix:/sockets/reference.sock"))
	assert.True(t, slices.Contains(process.command().Env, "SPECTRE_EGRESS=http+unix:/sockets/egress-reference.sock"))
	assert.Equal(t, newServiceLogWriter(os.Stdout, roleReference), process.command().Stdout.(*serviceLogWriter))
	assert.True(t, process.command().Stderr == os.Stderr, "service stderr is not passed through")
}

func TestServiceProcessCandidateSandboxed(t *testing.T) {
	s, _ := testSupervisor(t)
	process := s.serviceProcess(t.Context(), roleCandidate, "http+unix:/sockets/egress-candidate.sock", "unix:/sockets/candidate.sock", true)
	selfExe, err := os.Executable()
	assert.NoError(t, err)
	assert.Equal(t, []string{selfExe, SandboxArg, "/bin/sh", "-c", "run-candidate --port 1"}, process.command().Args)
	assert.True(t, slices.Contains(process.command().Env, "SPECTRE_EGRESS=http+unix:/sockets/egress-candidate.sock"))
}

func TestServiceStopStopsItsChildren(t *testing.T) {
	for name, child := range map[string]string{
		// The shell dies on SIGTERM without passing it on, as dash does for sh -c.
		"Terminated":  "sleep 300",
		"IgnoresTerm": "sh -c 'trap \"\" TERM; exec sleep 300'",
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := testSupervisor(t)
			s.config.ShutdownTimeout = 200 * time.Millisecond
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			s.config.Reference = child + " & echo $! >" + pidFile + "; wait"
			ctx, cancel := context.WithCancel(t.Context())
			process := s.serviceProcess(ctx, roleReference, "http+unix:/egress.sock", "unix:/reference.sock", false)
			assert.NoError(t, process.start())
			childPID := waitForPID(t, pidFile)

			cancel()
			assert.Error(t, process.wait())
			waitForExit(t, childPID)
		})
	}
}

// waitForPID reads the PID a service writes to path once it has started a child.
func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && convErr == nil {
			return pid
		}
		assert.True(t, time.Now().Before(deadline), "service never wrote its child's PID")
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForExit waits for pid to exit and be reaped.
func waitForExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		assert.True(t, time.Now().Before(deadline), "child %d outlived its service", pid)
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEgressConfig(t *testing.T) {
	s, config := testSupervisor(t)
	assert.NoError(t, s.prepareWiring())
	egress := s.egressConfig()
	assert.Equal(t, unixListen(filepath.Join(config.SocketDir, "egress-reference.sock")), egress.ReferenceListen)
	assert.Equal(t, unixListen(filepath.Join(config.SocketDir, "egress-candidate.sock")), egress.CandidateListen)
	assert.Equal(t, unixListen(filepath.Join(config.SocketDir, "egress-health.sock")), egress.HealthListen)
	assert.Equal(t, config.Destinations, egress.Destinations)
}

func TestIngressConfig(t *testing.T) {
	s, config := testSupervisor(t)
	assert.NoError(t, s.prepareWiring())
	ingress := s.ingressConfig()
	assert.Equal(t, config.Listen, ingress.Listen)
	assert.Equal(t, h2cUnix(filepath.Join(config.SocketDir, "reference.sock")), ingress.Reference)
	assert.Equal(t, h2cUnix(filepath.Join(config.SocketDir, "candidate.sock")), ingress.Candidate)
}

func TestIngressConfigProtocols(t *testing.T) {
	s, config := testSupervisor(t)
	s.config.ReferenceProtocol = protocolHTTP
	s.config.CandidateProtocol = "h2c"
	s.config.Reflection = false
	assert.NoError(t, s.prepareWiring())
	ingress := s.ingressConfig()
	assert.Equal(t, httpUnix(filepath.Join(config.SocketDir, "reference.sock")), ingress.Reference)
	assert.Equal(t, h2cUnix(filepath.Join(config.SocketDir, "candidate.sock")), ingress.Candidate)
	assert.False(t, ingress.Reflection)
}

func TestEngineConfig(t *testing.T) {
	s, _ := testSupervisor(t)
	s.config.ScriptsDir = "scripts"
	s.config.SchemaDir = "schema"
	s.config.MatchWindow = 3 * time.Second
	engine := s.engineConfig()
	assert.Equal(t, "scripts", engine.Comparison.ScriptsDir)
	assert.Equal(t, "schema", engine.Comparison.Schema.SchemaDir)
	assert.Equal(t, 3*time.Second, engine.MatchWindow)
}

func TestWaitListening(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	defer listener.Close()
	assert.NoError(t, waitListening(t.Context(), "tcp", listener.Addr().String(), time.Second))

	err = waitListening(t.Context(), "tcp", "127.0.0.1:1", 200*time.Millisecond)
	assert.Error(t, err)
}

func TestLogsTagSubsystem(t *testing.T) {
	for _, test := range []struct {
		name      string
		log       func(t *testing.T, ctx context.Context)
		subsystem string
	}{
		{name: "Supervisor", subsystem: "supervisor", log: func(t *testing.T, ctx context.Context) {
			t.Helper()
			s, err := New(ctx, testConfig(t), testCatalog(t))
			assert.NoError(t, err)
			s.log.InfoContext(ctx, "logged")
		}},
		{name: "Proxy", subsystem: "egress", log: func(t *testing.T, ctx context.Context) {
			t.Helper()
			proxyContext := withProxyLogger(ctx, "egress")
			logger.FromContext(proxyContext).InfoContext(proxyContext, "logged")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			ctx := logger.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, nil)))
			test.log(t, ctx)
			var record struct {
				Subsystem string `json:"spectre.subsystem"`
			}
			assert.NoError(t, json.Unmarshal(output.Bytes(), &record))
			assert.Equal(t, test.subsystem, record.Subsystem)
		})
	}
}

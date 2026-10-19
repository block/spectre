package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/logger"
)

func testSupervisor(t *testing.T) (*Supervisor, Config) {
	t.Helper()
	config := testConfig(t)
	s, err := New(t.Context(), config)
	assert.NoError(t, err)
	return s, config
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
	assert.True(t, process.command().Stderr == os.Stderr, "service stderr is not passed through")
}

func TestServiceOutputLabelled(t *testing.T) {
	for _, test := range []struct {
		name     string
		line     string
		expected map[string]any
	}{
		{name: "Text", line: "service line", expected: map[string]any{"level": "INFO", "msg": "service line"}},
		// Labels override a service's own subsystem so its lines stay attributable.
		{name: "Structured", line: `{"level":"warn","msg":"disk full","subsystem":"db","path":"/data"}`,
			expected: map[string]any{"level": "WARN", "msg": "disk full", "path": "/data"}},
		{name: "Adapted", line: `{"severity":"fatal","message":"disk full"}`,
			expected: map[string]any{"level": "ERROR", "msg": "disk full"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			ctx := logger.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, nil)))
			config := testConfig(t)
			config.ServiceLogs = logger.LineConfig{
				Fields: map[string]string{"message": "msg", "severity": "level"},
				Levels: map[string]slog.Level{"fatal": slog.LevelError},
			}
			s, err := New(ctx, config)
			assert.NoError(t, err)
			process := s.serviceProcess(ctx, roleCandidate, "http+unix:/sockets/egress-candidate.sock", "unix:/sockets/candidate.sock", false)
			_, err = io.WriteString(process.command().Stdout, test.line+"\n")
			assert.NoError(t, err)
			var record map[string]any
			assert.NoError(t, json.Unmarshal(output.Bytes(), &record))
			delete(record, "time")
			test.expected["subsystem"] = roleCandidate
			assert.Equal(t, test.expected, record)
		})
	}
}

func TestServiceProcessCandidateSandboxed(t *testing.T) {
	s, _ := testSupervisor(t)
	process := s.serviceProcess(t.Context(), roleCandidate, "http+unix:/sockets/egress-candidate.sock", "unix:/sockets/candidate.sock", true)
	selfExe, err := os.Executable()
	assert.NoError(t, err)
	assert.Equal(t, []string{selfExe, SandboxArg, "/bin/sh", "-c", "run-candidate --port 1"}, process.command().Args)
	assert.True(t, slices.Contains(process.command().Env, "SPECTRE_EGRESS=http+unix:/sockets/egress-candidate.sock"))
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
			s, err := New(ctx, testConfig(t))
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
				Subsystem string `json:"subsystem"`
			}
			assert.NoError(t, json.Unmarshal(output.Bytes(), &record))
			assert.Equal(t, test.subsystem, record.Subsystem)
		})
	}
}

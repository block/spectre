package supervisor

import (
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
	config := NewConfig()
	config.Candidate = "run-candidate --port 1"
	config.Reference = "run-reference --port 2"
	config.SocketDir = t.TempDir()
	config.Destinations = map[string]string{"b.example": "http://b", "a.example": "http://a"}
	s, err := New(config, logger.NewConfig(), slog.New(slog.DiscardHandler), io.Discard)
	assert.NoError(t, err)
	return s, config
}

func TestServiceProcessReference(t *testing.T) {
	s, _ := testSupervisor(t)
	process := s.serviceProcess(t.Context(), roleReference, "http+unix:/sockets/egress-reference.sock", "unix:/sockets/reference.sock", false)
	assert.Equal(t, []string{"/bin/sh", "-c", "run-reference --port 2"}, process.command().Args)
	assert.True(t, slices.Contains(process.command().Env, "SPECTRE_LISTEN=unix:/sockets/reference.sock"))
	assert.True(t, slices.Contains(process.command().Env, "SPECTRE_EGRESS=http+unix:/sockets/egress-reference.sock"))
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

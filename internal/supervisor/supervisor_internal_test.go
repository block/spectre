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

func TestEgressProcessArgs(t *testing.T) {
	s, config := testSupervisor(t)
	assert.NoError(t, s.prepareWiring())
	args := s.egressProcess(t.Context()).command().Args
	// The supervisor re-execs itself with the embedded egress subcommand.
	assert.Equal(t, s.selfExe, args[0])
	assert.Equal(t, EgressCommand, args[1])
	assert.True(t, slices.Contains(args, "--reference-listen="+unixListen(filepath.Join(config.SocketDir, "egress-reference.sock"))))
	assert.True(t, slices.Contains(args, "--candidate-listen="+unixListen(filepath.Join(config.SocketDir, "egress-candidate.sock"))))
	assert.True(t, slices.Contains(args, "--health-listen="+unixListen(filepath.Join(config.SocketDir, "egress-health.sock"))))
	// Destinations are passed in sorted order for deterministic arguments.
	assert.True(t, slices.Contains(args, "--destination=a.example=http://a"))
	assert.True(t, slices.Index(args, "--destination=a.example=http://a") < slices.Index(args, "--destination=b.example=http://b"))
	assert.True(t, slices.Contains(args, "--log-level=info"))
}

func TestIngressProcessArgs(t *testing.T) {
	s, config := testSupervisor(t)
	assert.NoError(t, s.prepareWiring())
	args := s.ingressProcess(t.Context()).command().Args
	// The supervisor re-execs itself with the embedded ingress subcommand.
	assert.Equal(t, s.selfExe, args[0])
	assert.Equal(t, IngressCommand, args[1])
	assert.True(t, slices.Contains(args, "--listen="+config.Listen))
	assert.True(t, slices.Contains(args, "--reference="+h2cUnix(filepath.Join(config.SocketDir, "reference.sock"))))
	assert.True(t, slices.Contains(args, "--candidate="+h2cUnix(filepath.Join(config.SocketDir, "candidate.sock"))))
}

func TestWaitListening(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	defer listener.Close()
	assert.NoError(t, waitListening(t.Context(), "tcp", listener.Addr().String(), time.Second))

	err = waitListening(t.Context(), "tcp", "127.0.0.1:1", 200*time.Millisecond)
	assert.Error(t, err)
}

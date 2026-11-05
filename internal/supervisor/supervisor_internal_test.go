package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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
	"github.com/block/spectre/internal/mirror"
	"github.com/block/spectre/internal/state"
)

func testSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	s, err := New(t.Context(), testConfig(t), testCatalog(t))
	assert.NoError(t, err)
	return s
}

func testCatalog(t *testing.T) *state.Catalog {
	t.Helper()
	catalog, err := state.NewCatalog(httpserver.Supports()...)
	assert.NoError(t, err)
	return catalog
}

func testConfig(t *testing.T) Config {
	t.Helper()
	config := NewConfig()
	config.Candidate = "run-candidate --port 1"
	config.Reference = "run-reference --port 2"
	config.SocketDir = t.TempDir()
	return config
}

func TestServiceProcessReference(t *testing.T) {
	s := testSupervisor(t)
	process := s.serviceProcess(t.Context(), roleReference, "http+unix:/sockets/egress-reference.sock", "unix:/sockets/reference.sock", false)
	assert.Equal(t, []string{"/bin/sh", "-c", "run-reference --port 2"}, process.command().Args)
	assert.True(t, slices.Contains(process.command().Env, "SPECTRE_LISTEN=unix:/sockets/reference.sock"))
	assert.True(t, slices.Contains(process.command().Env, "SPECTRE_EGRESS=http+unix:/sockets/egress-reference.sock"))
	assert.Equal(t, newServiceLogWriter(os.Stdout, roleReference), process.command().Stdout.(*serviceLogWriter))
	assert.True(t, process.command().Stderr == os.Stderr, "service stderr is not passed through")
}

func TestServiceProcessCandidateSandboxed(t *testing.T) {
	s := testSupervisor(t)
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
			s := testSupervisor(t)
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

func TestWiring(t *testing.T) {
	for name, test := range map[string]struct {
		reference string
		candidate string
		backend   func(path string) string
	}{
		"H2C":  {reference: "h2c", candidate: "h2c", backend: h2cUnix},
		"HTTP": {reference: protocolHTTP, candidate: protocolHTTP, backend: httpUnix},
	} {
		t.Run(name, func(t *testing.T) {
			config := testConfig(t)
			config.ReferenceProtocol = test.reference
			config.CandidateProtocol = test.candidate
			s, err := New(t.Context(), config, testCatalog(t))
			assert.NoError(t, err)
			socket := func(name string) string { return filepath.Join(config.SocketDir, name) }
			assert.Equal(t, mirror.Wiring{
				Listen:          config.Listen,
				Reference:       test.backend(socket("reference.sock")),
				Candidate:       test.backend(socket("candidate.sock")),
				ReferenceEgress: unixListen(socket("egress-reference.sock")),
				CandidateEgress: unixListen(socket("egress-candidate.sock")),
				EgressHealth:    unixListen(socket("egress-health.sock")),
				Health:          None[string](),
			}, s.wiring())
		})
	}
}

func TestLogsTagSubsystem(t *testing.T) {
	var output bytes.Buffer
	ctx := logger.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, nil)))
	s, err := New(ctx, testConfig(t), testCatalog(t))
	assert.NoError(t, err)
	s.log.InfoContext(ctx, "logged")
	var record struct {
		Subsystem string `json:"spectre.subsystem"`
	}
	assert.NoError(t, json.Unmarshal(output.Bytes(), &record))
	assert.Equal(t, "supervisor", record.Subsystem)
}

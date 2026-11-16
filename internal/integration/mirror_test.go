package integration_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/sample/pb/samplepbconnect"
)

const (
	// startTimeout matches the readiness timeouts in Procfile.integration.
	startTimeout = 90 * time.Second
	// logTimeout bounds how long a test waits for a process to log a record.
	logTimeout = 10 * time.Second
)

// mirror is one run of Procfile.integration, in a directory of its own so that
// tests running in parallel never share a socket.
type mirror struct {
	dir string
	log *proctorLog
}

// startMirror runs Procfile.integration with the candidate's sample data from
// candidateEnv, and waits until the proxy is ready. Proctor stops when the test ends.
func startMirror(t *testing.T) *mirror {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a whole mirror")
	}
	// A unix socket path must fit in 104 bytes, which t.TempDir can exceed.
	dir, err := os.MkdirTemp("/tmp", "spectre-") //nolint:usetesting // t.TempDir paths are too long for sockets.
	assert.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	// The Procfile names the sample's files relative to the repository root.
	assert.NoError(t, os.MkdirAll(filepath.Join(dir, "dist"), 0o750))
	assert.NoError(t, os.Symlink(filepath.Join(root, "internal"), filepath.Join(dir, "internal")))
	assert.NoError(t, os.Symlink(filepath.Join(root, "dist", "descriptors"), filepath.Join(dir, "dist", "descriptors")))

	m := &mirror{dir: dir, log: &proctorLog{}}
	cmd := exec.CommandContext(t.Context(), filepath.Join(root, "bin", "proctor"), filepath.Join(root, "Procfile.integration"))
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), candidateEnv(t)...)
	cmd.Stdout = m.log
	cmd.Stderr = m.log
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 20 * time.Second
	assert.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("proctor output:\n%s", m.log)
		}
	})
	m.await(t, startTimeout, "the proxy to be ready", func() bool { return m.log.ready("proxy") })
	return m
}

// waitFor waits until a process logs a record q matches.
func (m *mirror) waitFor(t *testing.T, q query) {
	t.Helper()
	m.waitForCount(t, q, 1)
}

// waitForCount waits until a process has logged at least count records q matches.
func (m *mirror) waitForCount(t *testing.T, q query, count int) {
	t.Helper()
	m.await(t, logTimeout, "a record matching "+q.String(), func() bool { return m.log.count(q) >= count })
}

// count returns how many records q matches so far.
func (m *mirror) count(q query) int {
	return m.log.count(q)
}

// await polls done until it holds, failing the test on timeout or when proctor
// reports a process failing.
func (m *mirror) await(t *testing.T, timeout time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !done() {
		if line, failed := m.log.failure(); failed {
			t.Fatalf("%s failed (%s) while waiting for %s", line.Process, line.Text, what)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// socket returns the path of one of the mirror's sockets.
func (m *mirror) socket(name string) string {
	return filepath.Join(m.dir, "dist", "sockets", name+".sock")
}

// users returns a gRPC client of the sample's user service through ingress.
func (m *mirror) users(t *testing.T) samplepbconnect.UserServiceClient {
	t.Helper()
	transport := unixTransport(t, m.socket("ingress"))
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport.Protocols = protocols
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	return samplepbconnect.NewUserServiceClient(connect.NewClient(connecthttp.NewTransport(client, "http://ingress", connecthttp.WithGRPC())))
}

// get fetches a URL over one of the mirror's sockets, returning the status and body.
func (m *mirror) get(t *testing.T, socket string, url string) (status int, body string) {
	t.Helper()
	client := &http.Client{Transport: unixTransport(t, m.socket(socket)), Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	assert.NoError(t, err)
	response, err := client.Do(request)
	assert.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	assert.NoError(t, err)
	return response.StatusCode, string(data)
}

// unixTransport returns a transport that connects every request to socket.
func unixTransport(t *testing.T, socket string) *http.Transport {
	t.Helper()
	var dialer net.Dialer
	transport := &http.Transport{DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return transport
}

package statsd_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/statsd"
)

// receiveTimeout bounds how long a test waits for a datagram.
const receiveTimeout = 5 * time.Second

func TestForwardSendsReferenceDatagramsUnchangedAndDiscardsCandidates(t *testing.T) {
	dir := shortSocketDir(t)
	agent := listen(t, filepath.Join(dir, "agent.sock"))
	reference := listen(t, filepath.Join(dir, "reference.sock"))
	candidate := listen(t, filepath.Join(dir, "candidate.sock"))
	ctx, cancel := context.WithCancel(t.Context())
	forwarded := make(chan error, 1)
	discarded := make(chan error, 1)
	go func() {
		forwarded <- statsd.Forward(ctx, reference, netaddr.ParsePacket("unix:"+filepath.Join(dir, "agent.sock")))
	}()
	go func() { discarded <- statsd.Discard(ctx, candidate) }()

	send(t, filepath.Join(dir, "candidate.sock"), "candidate.requests:1|c")
	datagram := "framework.http.response_codes:1|c|#code:200\nframework.http.response_times:12|ms"
	send(t, filepath.Join(dir, "reference.sock"), datagram)
	assert.Equal(t, datagram, read(t, agent))
	send(t, filepath.Join(dir, "reference.sock"), "second:1|c")
	assert.Equal(t, "second:1|c", read(t, agent))

	cancel()
	assert.NoError(t, <-forwarded)
	assert.NoError(t, <-discarded)
	_, _, err := reference.ReadFrom(make([]byte, 1))
	assert.IsError(t, err, net.ErrClosed)
}

func TestForwardReachesAnAgentThatStartsLate(t *testing.T) {
	dir := shortSocketDir(t)
	agentSocket := filepath.Join(dir, "agent.sock")
	reference := listen(t, filepath.Join(dir, "reference.sock"))
	ctx, cancel := context.WithCancel(t.Context())
	forwarded := make(chan error, 1)
	go func() { forwarded <- statsd.Forward(ctx, reference, netaddr.ParsePacket("unix:"+agentSocket)) }()
	send(t, filepath.Join(dir, "reference.sock"), "dropped:1|c")

	agent := listen(t, agentSocket)
	buffer := make([]byte, 1024)
	deadline := time.Now().Add(receiveTimeout)
	wait := time.Millisecond
	for {
		assert.True(t, time.Now().Before(deadline), "agent received no datagram")
		send(t, filepath.Join(dir, "reference.sock"), "late:1|c")
		assert.NoError(t, agent.SetReadDeadline(time.Now().Add(wait)))
		size, _, err := agent.ReadFrom(buffer)
		if err == nil {
			assert.Equal(t, "late:1|c", string(buffer[:size]))
			break
		}
		wait = min(2*wait, 100*time.Millisecond)
	}

	cancel()
	assert.NoError(t, <-forwarded)
}

// listen opens a unix datagram socket at path.
func listen(t *testing.T, path string) net.PacketConn {
	t.Helper()
	conn, err := netaddr.ParsePacket("unix:" + path).Listen(t.Context())
	assert.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// send writes datagram to the unix datagram socket at path.
func send(t *testing.T, path string, datagram string) {
	t.Helper()
	conn, err := netaddr.ParsePacket("unix:" + path).Dial(t.Context())
	assert.NoError(t, err)
	_, err = conn.Write([]byte(datagram))
	assert.NoError(t, err)
	assert.NoError(t, conn.Close())
}

// read returns the next datagram on conn.
func read(t *testing.T, conn net.PacketConn) string {
	t.Helper()
	assert.NoError(t, conn.SetReadDeadline(time.Now().Add(receiveTimeout)))
	buffer := make([]byte, 1024)
	size, _, err := conn.ReadFrom(buffer)
	assert.NoError(t, err)
	return string(buffer[:size])
}

// shortSocketDir returns a temporary directory with a path short enough for unix socket names.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "spectre") //nolint:usetesting // t.TempDir's base path is too long for unix sockets.
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(dir)) })
	return dir
}

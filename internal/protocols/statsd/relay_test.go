package statsd_test

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocols/statsd"
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
	ctx, logs := capturedLogs(t)
	ctx, cancel := context.WithCancel(ctx)
	forwarded := make(chan error, 1)
	go func() { forwarded <- statsd.Forward(ctx, reference, netaddr.ParsePacket("unix:"+agentSocket)) }()
	send(t, filepath.Join(dir, "reference.sock"), "dropped:1|c")
	awaitLog(t, logs, "Dropping reference StatsD metrics until the agent takes them")

	agent := listen(t, agentSocket)
	send(t, filepath.Join(dir, "reference.sock"), "late:1|c")
	assert.Equal(t, "late:1|c", read(t, agent))
	awaitLog(t, logs, "Forwarding reference StatsD metrics again")

	cancel()
	assert.NoError(t, <-forwarded)
}

// An agent that stops reading must neither stop the relay draining the reference nor hold up shutdown.
func TestForwardDropsWhileTheAgentStopsReading(t *testing.T) {
	dir := shortSocketDir(t)
	agentSocket := filepath.Join(dir, "agent.sock")
	listen(t, agentSocket)
	reference := listen(t, filepath.Join(dir, "reference.sock"))
	ctx, logs := capturedLogs(t)
	ctx, cancel := context.WithCancel(ctx)
	forwarded := make(chan error, 1)
	go func() { forwarded <- statsd.Forward(ctx, reference, netaddr.ParsePacket("unix:"+agentSocket)) }()

	client, err := netaddr.ParsePacket("unix:" + filepath.Join(dir, "reference.sock")).Dial(t.Context())
	assert.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	datagram := []byte(strings.Repeat("x", 512) + ":1|c")
	deadline := time.Now().Add(receiveTimeout)
	wait := time.Millisecond
	for dropped := false; !dropped; {
		assert.True(t, time.Now().Before(deadline), "relay never dropped a datagram")
		// The reference socket may refuse part of a burst, which the next burst makes up for.
		for range 64 {
			_, _ = client.Write(datagram)
		}
		select {
		case message := <-logs:
			dropped = message == "Dropping reference StatsD metrics until the agent takes them"
		case <-time.After(wait):
			wait = min(2*wait, 50*time.Millisecond)
		}
	}

	cancel()
	select {
	case err := <-forwarded:
		assert.NoError(t, err)
	case <-time.After(receiveTimeout):
		t.Fatal("relay did not stop while the agent was not reading")
	}
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

// capturedLogs returns a context whose logger sends each message to the returned channel.
func capturedLogs(t *testing.T) (context.Context, <-chan string) {
	t.Helper()
	messages := make(chan string, 1024)
	return logger.WithLogger(t.Context(), slog.New(messageHandler{messages: messages})), messages
}

// awaitLog waits until message is logged.
func awaitLog(t *testing.T, logs <-chan string, message string) {
	t.Helper()
	timeout := time.After(receiveTimeout)
	for {
		select {
		case logged := <-logs:
			if logged == message {
				return
			}
		case <-timeout:
			t.Fatalf("never logged %q", message)
		}
	}
}

// messageHandler sends each record's message to a channel, dropping it when the channel is full.
type messageHandler struct {
	messages chan<- string
}

func (h messageHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h messageHandler) Handle(_ context.Context, record slog.Record) error {
	select {
	case h.messages <- record.Message:
	default:
	}
	return nil
}

func (h messageHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h messageHandler) WithGroup(string) slog.Handler { return h }

// shortSocketDir returns a temporary directory with a path short enough for unix socket names.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "spectre") //nolint:usetesting // t.TempDir's base path is too long for unix sockets.
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(dir)) })
	return dir
}

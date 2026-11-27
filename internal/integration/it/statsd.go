//go:build integration

package it

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocols"
	statsdprotocol "github.com/block/spectre/internal/protocols/statsd"
	"github.com/block/spectre/internal/protocols/statsd/scripted"
)

const (
	statsdProtocol = scripted.Protocol
	// statsdWindow, statsdSettle keep the comparison of metrics fast.
	statsdWindow = 200 * time.Millisecond
	statsdSettle = 50 * time.Millisecond
)

// StatsD is the mirror's StatsD sockets: where each service sends its metrics, and
// the metrics agent that receives the reference's.
type StatsD struct {
	t         *testing.T
	reference string
	candidate string
	agent     net.PacketConn
	agentPath string
}

// WithStatsD relays the services' StatsD metrics, and compares them when scripts declare StatsD rules.
func WithStatsD() Setting {
	return func(setup *setup) { setup.statsd = true }
}

// newStatsD opens the StatsD sockets in a temporary directory, and returns them with the
// datagram listeners of the proxy.
func newStatsD(t *testing.T) (created *StatsD, listeners protocols.Listeners) {
	t.Helper()
	// Unix socket paths are too short for t.TempDir.
	dir, err := os.MkdirTemp("/tmp", "spectre") //nolint:usetesting // t.TempDir's base path is too long for unix sockets.
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(dir)) })
	created = &StatsD{
		t:         t,
		reference: filepath.Join(dir, "reference.sock"),
		candidate: filepath.Join(dir, "candidate.sock"),
		agent:     listenDatagrams(t, filepath.Join(dir, "agent.sock")),
		agentPath: filepath.Join(dir, "agent.sock"),
	}
	return created, protocols.Listeners{Datagrams: map[string]net.PacketConn{
		statsdprotocol.ReferenceListener: listenDatagrams(t, created.reference),
		statsdprotocol.CandidateListener: listenDatagrams(t, created.candidate),
	}}
}

// flags are the proxy's flags for the sockets, with a window short enough for tests.
func (s *StatsD) flags() []string {
	return []string{
		"--statsd-reference-listen=unix:" + s.reference,
		"--statsd-candidate-listen=unix:" + s.candidate,
		"--statsd-forward=unix:" + s.agentPath,
		"--statsd-window=" + statsdWindow.String(),
		"--statsd-settle=" + statsdSettle.String(),
		"--statsd-candidate-startup-timeout=0",
	}
}

// SendReference sends datagram to the socket the reference's metrics go to.
func (s *StatsD) SendReference(datagram string) {
	s.t.Helper()
	s.send(s.reference, datagram)
}

// SendCandidate sends datagram to the socket the candidate's metrics go to.
func (s *StatsD) SendCandidate(datagram string) {
	s.t.Helper()
	s.send(s.candidate, datagram)
}

// ExpectForwarded checks that the next datagram the metrics agent receives is expected.
func (s *StatsD) ExpectForwarded(expected string) {
	s.t.Helper()
	assert.NoError(s.t, s.agent.SetReadDeadline(time.Now().Add(waitTimeout)))
	buffer := make([]byte, 64*1024)
	size, _, err := s.agent.ReadFrom(buffer)
	assert.NoError(s.t, err)
	assert.Equal(s.t, expected, string(buffer[:size]))
}

func (s *StatsD) send(path, datagram string) {
	s.t.Helper()
	conn, err := netaddr.ParsePacket("unix:" + path).Dial(s.t.Context())
	assert.NoError(s.t, err)
	_, err = conn.Write([]byte(datagram))
	assert.NoError(s.t, err)
	assert.NoError(s.t, conn.Close())
}

// listenDatagrams opens a unix datagram socket at path, closed when the test ends.
func listenDatagrams(t *testing.T, path string) net.PacketConn {
	t.Helper()
	conn, err := netaddr.ParsePacket("unix:" + path).Listen(t.Context())
	assert.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// UnmatchedStatsD is the reason, without its detail, for a StatsD series on route that only one side sent.
func UnmatchedStatsD(route model.Route) model.QuarantineReason {
	return model.QuarantineReason{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: statsdProtocol, Route: route}),
	}
}

// DivergentStatsD is the reason, without its detail, for StatsD series on route whose statistics differ as
// differences describe.
func DivergentStatsD(route model.Route, differences ...model.Difference) model.QuarantineReason {
	return model.QuarantineReason{
		Kind:        model.QuarantineDivergent,
		Traffic:     Some(model.Traffic{Direction: model.Egress, Protocol: statsdProtocol, Route: route}),
		Differences: differences,
	}
}

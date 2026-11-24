package statsd_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/statsd"
)

func TestProviderListensOnTheConfiguredDatagramSockets(t *testing.T) {
	for name, test := range map[string]struct {
		config   statsd.Config
		expected []protocols.Listen
	}{
		"Disabled": {config: statsd.NewConfig(), expected: []protocols.Listen{}},
		"CandidateOnly": {config: statsd.Config{CandidateListen: "127.0.0.1:8125"}, expected: []protocols.Listen{
			{Name: "candidate", Address: "127.0.0.1:8125", Transport: protocols.Datagram},
		}},
		"Forwarded": {
			config: statsd.Config{ReferenceListen: "unix:@reference.sock", CandidateListen: "unix:@candidate.sock", Forward: "unix:@veneur.sock"},
			expected: []protocols.Listen{
				{Name: "reference", Address: "unix:@reference.sock", Transport: protocols.Datagram},
				{Name: "candidate", Address: "unix:@candidate.sock", Transport: protocols.Datagram},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.expected, statsd.NewProvider().Listens(test.config))
		})
	}
}

// Without listeners the server has nothing to relay, so it serves until the mirror stops.
func TestServeWithoutListenersStopsWithTheMirror(t *testing.T) {
	server, err := statsd.NewProvider().New(t.Context(), statsd.NewConfig(), protocols.Runtime{})
	assert.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, protocols.Listeners{}) }()

	cancel()
	select {
	case err := <-served:
		assert.NoError(t, err)
	case <-time.After(receiveTimeout):
		t.Fatal("server did not stop")
	}
}

func TestServeForwardsTheReferenceAndDiscardsTheCandidate(t *testing.T) {
	dir := shortSocketDir(t)
	agent := listen(t, dir+"/agent.sock")
	listeners := protocols.Listeners{Datagrams: map[string]net.PacketConn{
		statsd.ReferenceListener: listen(t, dir+"/reference.sock"),
		statsd.CandidateListener: listen(t, dir+"/candidate.sock"),
	}}
	config := statsd.Config{ReferenceListen: "unix:" + dir + "/reference.sock", CandidateListen: "unix:" + dir + "/candidate.sock", Forward: "unix:" + dir + "/agent.sock"}
	server, err := statsd.NewProvider().New(t.Context(), config, protocols.Runtime{})
	assert.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, listeners) }()

	send(t, dir+"/candidate.sock", "candidate:1|c")
	send(t, dir+"/reference.sock", "reference:1|c")
	assert.Equal(t, "reference:1|c", read(t, agent))

	cancel()
	assert.NoError(t, <-served)
}

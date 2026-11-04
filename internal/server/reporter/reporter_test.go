package reporter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/reporter"
	"github.com/block/spectre/internal/server/storage"
)

// updateProcedure is the protocol's path for streaming a mirror's state.
const updateProcedure = "/spectre.v1.SpectreService/Update"

// mirror is a reporter running against a SPECTRE server.
type mirror struct {
	spectre *client.Client
	// streams counts the streams the reporter opened.
	streams *atomic.Int32
	egress  chan model.Quarantine
}

// run runs a reporter against a server that lists states received within ttl
// and fails the first failures streams.
func run(t *testing.T, ttl time.Duration, failures int32, keepalive, maxAge time.Duration) mirror {
	t.Helper()
	streams := &atomic.Int32{}
	service := server.NewService(storage.NewMemory(), ttl).Handler()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == updateProcedure && streams.Add(1) <= failures {
			// Reply without reading the stream, as a proxy in front of the server does.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now()) //nolint:errcheck // HTTP/1 test server
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		service.ServeHTTP(w, request)
	}))
	t.Cleanup(httpServer.Close)
	config := reporter.Config{
		Client:            client.Config{URL: httpServer.URL},
		Service:           "users",
		Environment:       "production",
		Revision:          "abc123",
		KeepaliveInterval: keepalive,
		StreamMaxAge:      maxAge,
	}
	spectre := client.New(config.Client)
	egress := make(chan model.Quarantine, 1)
	ingress := make(chan model.Quarantine, 1)
	reporting := reporter.New(config, spectre, reporter.Source{Direction: model.Ingress, Quarantined: ingress}, reporter.Source{Direction: model.Egress, Quarantined: egress})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go reporting.Run(ctx)
	return mirror{spectre: spectre, streams: streams, egress: egress}
}

// eventually fails the test unless done returns true within timeout.
func eventually(t *testing.T, timeout time.Duration, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// states lists the states the server holds.
func (m mirror) states(t *testing.T) ([]model.State, []model.Divergence) {
	t.Helper()
	states, divergences, err := m.spectre.ListDivergences(t.Context(), model.DivergenceFilter{})
	assert.NoError(t, err)
	return states, divergences
}

func TestReporterReconnectsWithQuarantine(t *testing.T) {
	m := run(t, time.Hour, 1, 10*time.Millisecond, time.Hour)
	quarantine := model.Quarantine{Since: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Reason: model.QuarantineReason{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: "POST api.example/v1/charges"}),
		Detail:  "no matching reference call",
	}}
	m.egress <- quarantine

	// Whichever stream the quarantine reaches, the next one begins with it.
	eventually(t, 10*time.Second, func() bool {
		_, divergences := m.states(t)
		return len(divergences) > 0
	})
	assert.True(t, m.streams.Load() > 1)

	states, divergences := m.states(t)
	expected := []model.State{{
		Service:        "users",
		Environment:    "production",
		Revision:       "abc123",
		SpectreVersion: internal.Version,
		//nolint:exhaustive // Only the egress proxy quarantined.
		Quarantines: map[model.Direction]model.Quarantine{model.Egress: quarantine},
	}}
	assert.Equal(t, expected, states, assert.Exclude[time.Duration]())
	recorded := []model.Divergence{{Service: "users", Environment: "production", Revision: "abc123", Direction: model.Egress, Quarantine: quarantine}}
	assert.Equal(t, recorded, divergences)
}

func TestReporterKeepsQuietStreamListed(t *testing.T) {
	m := run(t, 100*time.Millisecond, 0, 20*time.Millisecond, time.Hour)
	eventually(t, 10*time.Second, func() bool {
		states, _ := m.states(t)
		return len(states) == 1
	})

	// Keepalives outlast the server's TTL on one stream.
	time.Sleep(500 * time.Millisecond)
	states, _ := m.states(t)
	assert.Equal(t, 1, len(states))
	assert.Equal(t, int32(1), m.streams.Load())
}

func TestReporterReplacesAgedStreamAtOnce(t *testing.T) {
	m := run(t, time.Hour, 0, time.Hour, 20*time.Millisecond)

	// Failed streams would back off for at least 1.5s before a third.
	eventually(t, time.Second, func() bool { return m.streams.Load() >= 3 })
}

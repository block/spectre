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
	"github.com/block/spectre/internal/stream"
)

// updateProcedure is the protocol's path for streaming a mirror's state.
const updateProcedure = "/spectre.v1.SpectreService/Update"

func TestReporterReconnectsWithQuarantine(t *testing.T) {
	var attempts atomic.Int32
	service := server.NewService(storage.NewMemory(), time.Hour).Handler()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		// The first stream fails, which the reporter recovers from.
		if request.URL.Path == updateProcedure && attempts.Add(1) == 1 {
			// Reply without reading the stream, as a proxy in front of the server does.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now()) //nolint:errcheck // HTTP/1 test server
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		service.ServeHTTP(w, request)
	}))
	t.Cleanup(httpServer.Close)
	ingress := make(chan model.Quarantine, 1)
	egress := make(chan model.Quarantine, 1)
	config := reporter.Config{
		Client:      client.Config{URL: httpServer.URL},
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
		Stream:      stream.Config{KeepaliveInterval: 10 * time.Millisecond, MaxAge: time.Hour},
	}
	spectre := client.New(config.Client)
	mirror := reporter.New(config, spectre, reporter.Source{Direction: model.Ingress, Quarantined: ingress}, reporter.Source{Direction: model.Egress, Quarantined: egress})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go mirror.Run(ctx)
	quarantine := model.Quarantine{Since: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Reason: model.QuarantineReason{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: "POST api.example/v1/charges"}),
		Detail:  "no matching reference call",
	}}
	egress <- quarantine

	// Whichever stream the quarantine reaches, the next one begins with it.
	deadline := time.After(10 * time.Second)
	for {
		_, divergences, err := spectre.ListDivergences(t.Context(), model.DivergenceFilter{})
		assert.NoError(t, err)
		if len(divergences) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the reporter never reported the quarantine")
		case <-time.After(10 * time.Millisecond):
		}
	}
	assert.True(t, attempts.Load() > 1)

	states, divergences, err := spectre.ListDivergences(t.Context(), model.DivergenceFilter{})
	assert.NoError(t, err)
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

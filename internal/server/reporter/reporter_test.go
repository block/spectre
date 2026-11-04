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

// updateProcedure is the protocol's path for sending a heartbeat.
const updateProcedure = "/spectre.v1.SpectreService/Update"

func TestReporterSendsHeartbeatOnQuarantine(t *testing.T) {
	heartbeats := make(chan int, 8)
	var attempts atomic.Int32
	service := server.NewService(storage.NewMemory(), time.Hour).Handler()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != updateProcedure {
			service.ServeHTTP(w, request)
			return
		}
		attempt := int(attempts.Add(1))
		// The first heartbeat fails, which the next one recovers from.
		if attempt == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		} else {
			service.ServeHTTP(w, request)
		}
		heartbeats <- attempt
	}))
	t.Cleanup(httpServer.Close)
	ingress := make(chan model.Quarantine, 1)
	egress := make(chan model.Quarantine, 1)
	config := reporter.Config{
		Client:            client.Config{URL: httpServer.URL},
		Service:           "users",
		Environment:       "production",
		Revision:          "abc123",
		HeartbeatInterval: time.Hour,
	}
	spectre := client.New(config.Client)
	mirror := reporter.New(config, spectre, reporter.Source{Direction: model.Ingress, Quarantined: ingress}, reporter.Source{Direction: model.Egress, Quarantined: egress})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go mirror.Run(ctx)
	assert.Equal(t, 1, <-heartbeats)

	quarantine := model.Quarantine{Since: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Reason: model.QuarantineReason{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: "POST api.example/v1/charges"}),
		Detail:  "no matching reference call",
	}}
	egress <- quarantine
	assert.Equal(t, 2, <-heartbeats)

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

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

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/reporter"
	"github.com/block/spectre/internal/server/storage"
)

// quarantineProcedure is the protocol's path for reporting a quarantine.
const quarantineProcedure = "/spectre.v1.SpectreService/Quarantine"

func TestReporterRetriesQuarantine(t *testing.T) {
	reports := make(chan struct{}, 8)
	var attempts atomic.Int32
	service := server.NewService(storage.NewMemory()).Handler()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == quarantineProcedure && attempts.Add(1) == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		service.ServeHTTP(w, request)
		if request.URL.Path == quarantineProcedure {
			reports <- struct{}{}
		}
	}))
	t.Cleanup(httpServer.Close)
	ingress := make(chan model.Quarantine, 1)
	egress := make(chan model.Quarantine, 1)
	config := reporter.Config{
		Client:      client.Config{URL: httpServer.URL},
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
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
	<-reports

	quarantined, err := spectre.ListDivergences(t.Context(), model.DivergenceFilter{})
	assert.NoError(t, err)
	expected := []model.Divergence{{
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
		Direction:   model.Egress,
		Quarantine:  quarantine,
	}}
	assert.Equal(t, expected, quarantined)
	assert.Equal(t, int32(2), attempts.Load())
}

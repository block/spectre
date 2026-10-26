package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/quarantine"
	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/terminal"
	pb "github.com/block/spectre/spectrepb"
	"github.com/block/spectre/spectrepb/spectrepbconnect"
)

func TestDivergencesKeepLatestQuarantine(t *testing.T) {
	httpServer := httptest.NewServer(server.NewService().Handler())
	t.Cleanup(httpServer.Close)
	client := spectrepbconnect.NewSpectreServiceClient(httpServer.Client(), httpServer.URL)
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	divergent := func(since time.Time, route string) *pb.Divergence {
		return &pb.Divergence{
			Service:     "users",
			Environment: "production",
			Revision:    "abc123",
			Direction:   pb.Direction_DIRECTION_INGRESS,
			Quarantine: &pb.Quarantine{
				Since:       timestamppb.New(since),
				Kind:        pb.Quarantine_KIND_DIVERGENT,
				Traffic:     &pb.Traffic{Direction: pb.Direction_DIRECTION_INGRESS, Protocol: "http", Route: route},
				Node:        proto.String("users"),
				Differences: []string{"body.name"},
			},
		}
	}
	unmatched := &pb.Divergence{
		Service:     "users",
		Environment: "staging",
		Revision:    "abc123",
		Direction:   pb.Direction_DIRECTION_EGRESS,
		Quarantine: &pb.Quarantine{
			Since:  timestamppb.New(first),
			Kind:   pb.Quarantine_KIND_UNMATCHED,
			Detail: "no matching reference call",
		},
	}
	report := func(divergence *pb.Divergence) {
		_, err := client.Quarantine(t.Context(), connect.NewRequest(&pb.QuarantineRequest{Divergence: divergence}))
		assert.NoError(t, err)
	}
	report(unmatched)
	report(divergent(first, "GET /v1/users/{id}"))
	// A retry repeats the same quarantine.
	report(divergent(first, "GET /v1/users/{id}"))
	// A restarted mirror quarantines again, then a stale retry arrives.
	latest := divergent(first.Add(time.Minute), "GET /v1/users")
	report(latest)
	report(divergent(first.Add(-time.Minute), "GET /v1/users/{id}/avatar"))

	expected := &pb.ListDivergencesResponse{Divergences: []*pb.Divergence{latest, unmatched}}
	all, err := client.ListDivergences(t.Context(), connect.NewRequest(&pb.ListDivergencesRequest{}))
	assert.NoError(t, err)
	assert.True(t, proto.Equal(expected, all.Msg), "%v", all.Msg)

	staging, err := client.ListDivergences(t.Context(), connect.NewRequest(&pb.ListDivergencesRequest{Environment: proto.String("staging")}))
	assert.NoError(t, err)
	assert.True(t, proto.Equal(&pb.ListDivergencesResponse{Divergences: []*pb.Divergence{unmatched}}, staging.Msg), "%v", staging.Msg)

	var text strings.Builder
	config := server.DivergencesConfig{Query: server.QueryConfig{Client: server.ClientConfig{URL: httpServer.URL}}}
	assert.NoError(t, server.WriteDivergences(t.Context(), &text, config, terminal.NewPalette(false)))
	assert.Equal(t, `users abc123 production
  ingress http GET /v1/users: response differences: body.name (node users)
  quarantined 2026-10-01T12:01:00Z
users abc123 staging
  egress: no matching reference call
  quarantined 2026-10-01T12:00:00Z
`, text.String())

	var data strings.Builder
	config.Query.JSON = true
	config.Node = "users"
	assert.NoError(t, server.WriteDivergences(t.Context(), &data, config, terminal.NewPalette(false)))
	decoded := &pb.ListDivergencesResponse{}
	assert.NoError(t, protojson.Unmarshal([]byte(data.String()), decoded))
	assert.True(t, proto.Equal(&pb.ListDivergencesResponse{Divergences: []*pb.Divergence{latest}}, decoded), "%v", decoded)
}

func TestClearDivergencesOfServiceEnvironment(t *testing.T) {
	httpServer := httptest.NewServer(server.NewService().Handler())
	t.Cleanup(httpServer.Close)
	client := spectrepbconnect.NewSpectreServiceClient(httpServer.Client(), httpServer.URL)
	divergence := func(environment string, direction pb.Direction) *pb.Divergence {
		return &pb.Divergence{
			Service:     "users",
			Environment: environment,
			Revision:    "abc123",
			Direction:   direction,
			Quarantine:  &pb.Quarantine{Since: timestamppb.Now(), Kind: pb.Quarantine_KIND_CAPACITY},
		}
	}
	staging := divergence("staging", pb.Direction_DIRECTION_INGRESS)
	for _, reported := range []*pb.Divergence{
		divergence("production", pb.Direction_DIRECTION_INGRESS),
		divergence("production", pb.Direction_DIRECTION_EGRESS),
		staging,
	} {
		_, err := client.Quarantine(t.Context(), connect.NewRequest(&pb.QuarantineRequest{Divergence: reported}))
		assert.NoError(t, err)
	}

	config := server.ClearConfig{Client: server.ClientConfig{URL: httpServer.URL}, Service: "users", Environment: "production"}
	assert.NoError(t, server.ClearDivergences(t.Context(), config))

	remaining, err := client.ListDivergences(t.Context(), connect.NewRequest(&pb.ListDivergencesRequest{}))
	assert.NoError(t, err)
	assert.True(t, proto.Equal(&pb.ListDivergencesResponse{Divergences: []*pb.Divergence{staging}}, remaining.Msg), "%v", remaining.Msg)

	_, err = client.ClearDivergences(t.Context(), connect.NewRequest(&pb.ClearDivergencesRequest{Service: "users"}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestQuarantineRejectsInvalidDivergence(t *testing.T) {
	httpServer := httptest.NewServer(server.NewService().Handler())
	t.Cleanup(httpServer.Close)
	client := spectrepbconnect.NewSpectreServiceClient(httpServer.Client(), httpServer.URL)
	valid := func() *pb.Divergence {
		return &pb.Divergence{
			Service:     "users",
			Environment: "production",
			Revision:    "abc123",
			Direction:   pb.Direction_DIRECTION_INGRESS,
			Quarantine:  &pb.Quarantine{Since: timestamppb.Now(), Kind: pb.Quarantine_KIND_UNMATCHED},
		}
	}
	withoutRevision := valid()
	withoutRevision.Revision = ""
	withoutDirection := valid()
	withoutDirection.Direction = pb.Direction_DIRECTION_UNSPECIFIED
	withoutQuarantine := valid()
	withoutQuarantine.Quarantine = nil
	withoutKind := valid()
	withoutKind.Quarantine.Kind = pb.Quarantine_KIND_UNSPECIFIED
	withoutTrafficDirection := valid()
	withoutTrafficDirection.Quarantine.Traffic = &pb.Traffic{Protocol: "http"}
	tests := []struct {
		name       string
		divergence *pb.Divergence
	}{
		{name: "WithoutRevision", divergence: withoutRevision},
		{name: "UnspecifiedDirection", divergence: withoutDirection},
		{name: "WithoutQuarantine", divergence: withoutQuarantine},
		{name: "UnspecifiedKind", divergence: withoutKind},
		{name: "UnspecifiedTrafficDirection", divergence: withoutTrafficDirection},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.Quarantine(t.Context(), connect.NewRequest(&pb.QuarantineRequest{Divergence: test.divergence}))
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestReporterRetriesQuarantine(t *testing.T) {
	reports := make(chan struct{}, 8)
	var attempts atomic.Int32
	service := server.NewService().Handler()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == spectrepbconnect.SpectreServiceQuarantineProcedure && attempts.Add(1) == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		service.ServeHTTP(w, request)
		if request.URL.Path == spectrepbconnect.SpectreServiceQuarantineProcedure {
			reports <- struct{}{}
		}
	}))
	t.Cleanup(httpServer.Close)
	ingress := make(chan proxy.QuarantineStatus, 1)
	egress := make(chan proxy.QuarantineStatus, 1)
	config := server.ReporterConfig{
		Client:      server.ClientConfig{URL: httpServer.URL},
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
	}
	reporter := server.NewReporter(config, server.Source{Direction: boundary.Ingress, Quarantined: ingress}, server.Source{Direction: boundary.Egress, Quarantined: egress})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go reporter.Run(ctx)

	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	egress <- proxy.QuarantineStatus{Since: since, Reason: quarantine.Reason{
		Kind:    quarantine.KindUnmatched,
		Traffic: Some(quarantine.Traffic{Direction: boundary.Egress, Protocol: boundary.ProtocolHTTP, Route: "POST api.example/v1/charges"}),
		Detail:  "no matching reference call",
	}}
	<-reports

	client := spectrepbconnect.NewSpectreServiceClient(httpServer.Client(), httpServer.URL)
	quarantined, err := client.ListDivergences(t.Context(), connect.NewRequest(&pb.ListDivergencesRequest{}))
	assert.NoError(t, err)
	expected := &pb.ListDivergencesResponse{Divergences: []*pb.Divergence{{
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
		Direction:   pb.Direction_DIRECTION_EGRESS,
		Quarantine: &pb.Quarantine{
			Since:   timestamppb.New(since),
			Kind:    pb.Quarantine_KIND_UNMATCHED,
			Traffic: &pb.Traffic{Direction: pb.Direction_DIRECTION_EGRESS, Protocol: "http", Route: "POST api.example/v1/charges"},
			Detail:  "no matching reference call",
		},
	}}}
	assert.True(t, proto.Equal(expected, quarantined.Msg), "%v", quarantined.Msg)
	assert.Equal(t, int32(2), attempts.Load())
}

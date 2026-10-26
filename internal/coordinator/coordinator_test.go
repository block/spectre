package coordinator_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/coordinator"
	pb "github.com/block/spectre/internal/coordinator/pb"
	"github.com/block/spectre/internal/coordinator/pb/coordinatorpbconnect"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/quarantine"
	"github.com/block/spectre/internal/terminal"
)

func TestDivergencesGroupIdenticalQuarantines(t *testing.T) {
	server := httptest.NewServer(coordinator.NewService().Handler())
	t.Cleanup(server.Close)
	mirrors := coordinatorpbconnect.NewMirrorServiceClient(server.Client(), server.URL)
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	divergent := func(since time.Time, detail string) *pb.Quarantine {
		return &pb.Quarantine{
			Since:       timestamppb.New(since),
			Kind:        string(quarantine.KindDivergent),
			Traffic:     &pb.Traffic{Direction: "ingress", Protocol: "http", Route: "GET /v1/users/{id}"},
			Node:        proto.String("users"),
			Differences: []string{"body.name"},
			Detail:      detail,
		}
	}
	unmatched := &pb.Quarantine{
		Since:  timestamppb.New(first),
		Kind:   string(quarantine.KindUnmatched),
		Detail: "no matching reference call",
	}
	report := func(instance, environment string, quarantine *pb.Quarantine) {
		mirror := &pb.Mirror{
			Instance:    instance,
			Service:     "users",
			Environment: environment,
			Revision:    "abc123",
			Sources:     []*pb.Source{{Name: "ingress", Quarantine: quarantine}},
		}
		_, err := mirrors.Report(t.Context(), connect.NewRequest(&pb.ReportRequest{Mirror: mirror}))
		assert.NoError(t, err)
	}
	report("mirror-a", "production", divergent(first, "request 1"))
	// A heartbeat resends the same quarantine, which must not count twice.
	report("mirror-a", "production", divergent(first, "request 1"))
	report("mirror-b", "production", divergent(first.Add(time.Minute), "request 2"))
	report("mirror-c", "staging", unmatched)
	report("mirror-d", "staging", nil)

	expected := &pb.ListDivergencesResponse{Divergences: []*pb.Divergence{
		{
			Service:     "users",
			Environment: "production",
			Revision:    "abc123",
			Latest:      divergent(first.Add(time.Minute), "request 2"),
			Mirrors:     2,
			FirstSeen:   timestamppb.New(first),
		},
		{
			Service:     "users",
			Environment: "staging",
			Revision:    "abc123",
			Latest:      unmatched,
			Mirrors:     1,
			FirstSeen:   timestamppb.New(first),
		},
	}}
	queries := coordinatorpbconnect.NewQueryServiceClient(server.Client(), server.URL)
	all, err := queries.ListDivergences(t.Context(), connect.NewRequest(&pb.ListDivergencesRequest{}))
	assert.NoError(t, err)
	assert.True(t, proto.Equal(expected, all.Msg), "%v", all.Msg)

	staging, err := queries.ListDivergences(t.Context(), connect.NewRequest(&pb.ListDivergencesRequest{Environment: "staging"}))
	assert.NoError(t, err)
	assert.True(t, proto.Equal(&pb.ListDivergencesResponse{Divergences: expected.GetDivergences()[1:]}, staging.Msg), "%v", staging.Msg)

	var text strings.Builder
	config := coordinator.DivergencesConfig{Query: coordinator.QueryConfig{Client: coordinator.ClientConfig{URL: server.URL}}, Node: "users"}
	assert.NoError(t, coordinator.WriteDivergences(t.Context(), &text, config, terminal.NewPalette(false)))
	assert.Equal(t, `users abc123 production
  ingress http GET /v1/users/{id}: response differences: body.name (node users)
  2 mirrors, first seen 2026-10-01T12:00:00Z, last seen 2026-10-01T12:01:00Z
`, text.String())

	var data strings.Builder
	config.Query.JSON = true
	assert.NoError(t, coordinator.WriteDivergences(t.Context(), &data, config, terminal.NewPalette(false)))
	decoded := &pb.ListDivergencesResponse{}
	assert.NoError(t, protojson.Unmarshal([]byte(data.String()), decoded))
	assert.True(t, proto.Equal(&pb.ListDivergencesResponse{Divergences: expected.GetDivergences()[:1]}, decoded), "%v", decoded)
}

func TestReportRejectsIncompleteMirror(t *testing.T) {
	server := httptest.NewServer(coordinator.NewService().Handler())
	t.Cleanup(server.Close)
	mirrors := coordinatorpbconnect.NewMirrorServiceClient(server.Client(), server.URL)

	_, err := mirrors.Report(t.Context(), connect.NewRequest(&pb.ReportRequest{Mirror: &pb.Mirror{Service: "users"}}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestReporterReportsQuarantineImmediately(t *testing.T) {
	reports := make(chan struct{}, 8)
	service := coordinator.NewService().Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		service.ServeHTTP(w, request)
		if request.URL.Path == coordinatorpbconnect.MirrorServiceReportProcedure {
			reports <- struct{}{}
		}
	}))
	t.Cleanup(server.Close)
	ingress := proxy.NewCandidates(proxy.CandidatesConfig{CandidateMaxInFlight: 1})
	egress := proxy.NewCandidates(proxy.CandidatesConfig{CandidateMaxInFlight: 1})
	config := coordinator.ReporterConfig{
		Client:      coordinator.ClientConfig{URL: server.URL},
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
		// The heartbeat never fires during the test, so only a quarantine reports again.
		Heartbeat: time.Hour,
	}
	reporter, err := coordinator.NewReporter(config, coordinator.Source{Name: "ingress", Candidates: ingress}, coordinator.Source{Name: "egress", Candidates: egress})
	assert.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go reporter.Run(ctx)
	<-reports

	queries := coordinatorpbconnect.NewQueryServiceClient(server.Client(), server.URL)
	healthy, err := queries.ListDivergences(t.Context(), connect.NewRequest(&pb.ListDivergencesRequest{}))
	assert.NoError(t, err)
	assert.Equal(t, 0, len(healthy.Msg.GetDivergences()))

	egress.Quarantine(t.Context(), quarantine.Reason{
		Kind:    quarantine.KindUnmatched,
		Traffic: Some(quarantine.Traffic{Direction: boundary.Egress, Protocol: boundary.ProtocolHTTP, Route: "POST api.example/v1/charges"}),
		Detail:  "no matching reference call",
	})
	<-reports

	status, ok := egress.QuarantineStatus().Get()
	assert.True(t, ok)
	quarantined, err := queries.ListDivergences(t.Context(), connect.NewRequest(&pb.ListDivergencesRequest{}))
	assert.NoError(t, err)
	expected := &pb.ListDivergencesResponse{Divergences: []*pb.Divergence{{
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
		Latest: &pb.Quarantine{
			Since:   timestamppb.New(status.Since),
			Kind:    string(quarantine.KindUnmatched),
			Traffic: &pb.Traffic{Direction: "egress", Protocol: "http", Route: "POST api.example/v1/charges"},
			Detail:  "no matching reference call",
		},
		Mirrors:   1,
		FirstSeen: timestamppb.New(status.Since),
	}}}
	assert.True(t, proto.Equal(expected, quarantined.Msg), "%v", quarantined.Msg)
}

func TestReporterConfigValidate(t *testing.T) {
	complete := coordinator.ReporterConfig{
		Client:      coordinator.ClientConfig{URL: "http://coordinator"},
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
		Heartbeat:   time.Second,
	}
	withoutRevision := complete
	withoutRevision.Revision = ""
	withoutHeartbeat := complete
	withoutHeartbeat.Heartbeat = 0
	tests := []struct {
		name   string
		config coordinator.ReporterConfig
		err    string
	}{
		{name: "Disabled", config: coordinator.ReporterConfig{}},
		{name: "Complete", config: complete},
		{name: "WithoutRevision", config: withoutRevision, err: "a mirror reporting to the coordination service needs its service, environment, and revision"},
		{name: "WithoutHeartbeat", config: withoutHeartbeat, err: "coordinator heartbeat must be positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if test.err == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, test.err)
		})
	}
}

package server_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/server/storage"
	pb "github.com/block/spectre/spectrepb"
	"github.com/block/spectre/spectrepb/spectrepbconnect"
)

func newRPC(t *testing.T) spectrepbconnect.SpectreServiceClient {
	t.Helper()
	httpServer := httptest.NewServer(server.NewService(storage.NewMemory(), time.Hour).Handler())
	t.Cleanup(httpServer.Close)
	return spectrepbconnect.NewSpectreServiceClient(connect.NewClient(connecthttp.NewTransport(httpServer.Client(), httpServer.URL)))
}

func state(environment string, uptime time.Duration, quarantines ...*pb.Quarantine) *pb.State {
	return &pb.State{
		Service:         "users",
		Environment:     environment,
		CandidateCommit: "abc123",
		SpectreVersion:  "v1.2.3",
		Uptime:          durationpb.New(uptime),
		Quarantines:     quarantines,
	}
}

func TestHeartbeatKeepsLatestState(t *testing.T) {
	rpc := newRPC(t)
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	latest := state("production", 2*time.Minute, &pb.Quarantine{
		Direction:   pb.Direction_DIRECTION_INGRESS,
		Since:       timestamppb.New(since),
		Kind:        pb.Quarantine_KIND_DIVERGENT,
		Traffic:     &pb.Traffic{Direction: pb.Direction_DIRECTION_INGRESS, Protocol: "http", Route: "GET /v1/users"},
		Node:        proto.String("users"),
		Differences: []string{"body.name"},
	})
	staging := state("staging", time.Minute)
	for _, sent := range []*pb.State{state("production", time.Minute), staging, latest} {
		_, err := rpc.Heartbeat(t.Context(), &pb.HeartbeatRequest{State: sent})
		assert.NoError(t, err)
	}

	tests := []struct {
		name     string
		request  *pb.ListDivergencesRequest
		expected []*pb.State
	}{
		{name: "All", request: &pb.ListDivergencesRequest{}, expected: []*pb.State{latest, staging}},
		{name: "Environment", request: &pb.ListDivergencesRequest{Environment: proto.String("staging")}, expected: []*pb.State{staging}},
		{name: "Node", request: &pb.ListDivergencesRequest{Node: proto.String("users")}, expected: []*pb.State{latest}},
		{name: "CandidateCommit", request: &pb.ListDivergencesRequest{CandidateCommit: proto.String("def456")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listed, err := rpc.ListDivergences(t.Context(), test.request)
			assert.NoError(t, err)
			expected := &pb.ListDivergencesResponse{States: test.expected}
			assert.True(t, proto.Equal(expected, listed), "%v", listed)
		})
	}
}

func TestHeartbeatRejectsInvalidState(t *testing.T) {
	rpc := newRPC(t)
	quarantine := func(direction pb.Direction) *pb.Quarantine {
		return &pb.Quarantine{Direction: direction, Since: timestamppb.Now(), Kind: pb.Quarantine_KIND_UNMATCHED}
	}
	withoutCommit := state("production", time.Minute)
	withoutCommit.CandidateCommit = ""
	withoutUptime := state("production", time.Minute)
	withoutUptime.Uptime = nil
	withoutKind := state("production", time.Minute, quarantine(pb.Direction_DIRECTION_INGRESS))
	withoutKind.Quarantines[0].Kind = pb.Quarantine_KIND_UNSPECIFIED
	withoutTrafficDirection := state("production", time.Minute, quarantine(pb.Direction_DIRECTION_INGRESS))
	withoutTrafficDirection.Quarantines[0].Traffic = &pb.Traffic{Protocol: "http"}
	tests := []struct {
		name  string
		state *pb.State
	}{
		{name: "WithoutState"},
		{name: "WithoutCandidateCommit", state: withoutCommit},
		{name: "WithoutUptime", state: withoutUptime},
		{name: "UnspecifiedQuarantineDirection", state: state("production", time.Minute, quarantine(pb.Direction_DIRECTION_UNSPECIFIED))},
		{name: "RepeatedDirection", state: state("production", time.Minute, quarantine(pb.Direction_DIRECTION_INGRESS), quarantine(pb.Direction_DIRECTION_INGRESS))},
		{name: "UnspecifiedKind", state: withoutKind},
		{name: "UnspecifiedTrafficDirection", state: withoutTrafficDirection},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := rpc.Heartbeat(t.Context(), &pb.HeartbeatRequest{State: test.state})
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

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
		Service:        "users",
		Environment:    environment,
		Revision:       "abc123",
		SpectreVersion: "v1.2.3",
		Uptime:         durationpb.New(uptime),
		Quarantines:    quarantines,
	}
}

func TestHeartbeatKeepsLatestState(t *testing.T) {
	rpc := newRPC(t)
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	quarantine := &pb.Quarantine{
		Direction:   pb.Direction_DIRECTION_INGRESS,
		Since:       timestamppb.New(since),
		Kind:        pb.Quarantine_KIND_DIVERGENT,
		Traffic:     &pb.Traffic{Direction: pb.Direction_DIRECTION_INGRESS, Protocol: "http", Route: "GET /v1/users"},
		Node:        proto.String("users"),
		Differences: []string{"body.name"},
	}
	latest := state("production", 2*time.Minute, quarantine)
	divergence := &pb.Divergence{Service: "users", Environment: "production", Revision: "abc123", Quarantine: quarantine}
	staging := state("staging", time.Minute)
	for _, sent := range []*pb.State{state("production", time.Minute), staging, latest} {
		_, err := rpc.Heartbeat(t.Context(), &pb.HeartbeatRequest{State: sent})
		assert.NoError(t, err)
	}

	all := &pb.ListDivergencesResponse{States: []*pb.State{latest, staging}, Divergences: []*pb.Divergence{divergence}}
	tests := []struct {
		name     string
		request  *pb.ListDivergencesRequest
		expected *pb.ListDivergencesResponse
	}{
		{name: "All", request: &pb.ListDivergencesRequest{}, expected: all},
		{name: "Environment", request: &pb.ListDivergencesRequest{Environment: proto.String("staging")}, expected: &pb.ListDivergencesResponse{States: []*pb.State{staging}}},
		{name: "Node", request: &pb.ListDivergencesRequest{Node: proto.String("users")}, expected: &pb.ListDivergencesResponse{States: []*pb.State{latest}, Divergences: []*pb.Divergence{divergence}}},
		{name: "Revision", request: &pb.ListDivergencesRequest{Revision: proto.String("def456")}, expected: &pb.ListDivergencesResponse{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listed, err := rpc.ListDivergences(t.Context(), test.request)
			assert.NoError(t, err)
			assert.True(t, proto.Equal(test.expected, listed), "%v", listed)
		})
	}
}

func TestDivergencesOutliveRestartUntilCleared(t *testing.T) {
	rpc := newRPC(t)
	quarantine := &pb.Quarantine{Direction: pb.Direction_DIRECTION_EGRESS, Since: timestamppb.Now(), Kind: pb.Quarantine_KIND_UNMATCHED}
	quarantined := state("production", time.Minute, quarantine)
	redeployed := state("production", time.Second)
	redeployed.Revision = "def456"
	heartbeat := func(sent *pb.State) {
		_, err := rpc.Heartbeat(t.Context(), &pb.HeartbeatRequest{State: sent})
		assert.NoError(t, err)
	}
	list := func() *pb.ListDivergencesResponse {
		listed, err := rpc.ListDivergences(t.Context(), &pb.ListDivergencesRequest{})
		assert.NoError(t, err)
		return listed
	}
	heartbeat(quarantined)
	heartbeat(redeployed)
	divergence := &pb.Divergence{Service: "users", Environment: "production", Revision: "abc123", Quarantine: quarantine}
	expected := &pb.ListDivergencesResponse{States: []*pb.State{redeployed}, Divergences: []*pb.Divergence{divergence}}
	assert.True(t, proto.Equal(expected, list()), "%v", list())

	_, err := rpc.ClearDivergences(t.Context(), &pb.ClearDivergencesRequest{Service: "users", Environment: "production"})
	assert.NoError(t, err)
	// A heartbeat that was in flight still carries the cleared quarantine.
	heartbeat(quarantined)
	heartbeat(redeployed)
	assert.True(t, proto.Equal(&pb.ListDivergencesResponse{States: []*pb.State{redeployed}}, list()), "%v", list())

	_, err = rpc.ClearDivergences(t.Context(), &pb.ClearDivergencesRequest{Service: "users"})
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestHeartbeatRejectsInvalidState(t *testing.T) {
	rpc := newRPC(t)
	quarantine := func(direction pb.Direction) *pb.Quarantine {
		return &pb.Quarantine{Direction: direction, Since: timestamppb.Now(), Kind: pb.Quarantine_KIND_UNMATCHED}
	}
	withoutCommit := state("production", time.Minute)
	withoutCommit.Revision = ""
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
		{name: "WithoutRevision", state: withoutCommit},
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

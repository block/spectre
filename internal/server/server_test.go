package server_test

import (
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
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
	config := server.NewServeConfig()
	config.StateTTL = time.Hour
	httpServer := httptest.NewServer(server.NewService(storage.NewMemory(), config).Handler())
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

// update streams requests to the server, then closes the stream and returns
// the error the server ended it with.
func update(t *testing.T, rpc spectrepbconnect.SpectreServiceClient, requests ...*pb.UpdateRequest) error {
	t.Helper()
	stream, err := rpc.Update(t.Context())
	assert.NoError(t, err)
	for _, request := range requests {
		// A send fails with io.EOF once the server has ended the stream.
		if err := stream.Send(request); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
	}
	_, err = stream.CloseAndReceive()
	return err
}

func whole(state *pb.State) *pb.UpdateRequest {
	return &pb.UpdateRequest{Update: &pb.UpdateRequest_State{State: state}}
}

func quarantined(quarantine *pb.Quarantine) *pb.UpdateRequest {
	return &pb.UpdateRequest{Update: &pb.UpdateRequest_Quarantine{Quarantine: quarantine}}
}

func called(calls *pb.Calls) *pb.UpdateRequest {
	return &pb.UpdateRequest{Update: &pb.UpdateRequest_Calls{Calls: calls}}
}

func keepalive() *pb.UpdateRequest {
	return &pb.UpdateRequest{Update: &pb.UpdateRequest_Keepalive{Keepalive: &pb.Keepalive{}}}
}

// list returns what the server lists for request without uptimes, which it
// returns separately because the server advances them, or call total starts.
func list(t *testing.T, rpc spectrepbconnect.SpectreServiceClient, request *pb.ListDivergencesRequest) (listed *pb.ListDivergencesResponse, uptimes []time.Duration) {
	t.Helper()
	response, err := rpc.ListDivergences(t.Context(), request)
	assert.NoError(t, err)
	for _, listedState := range response.GetStates() {
		uptimes = append(uptimes, listedState.GetUptime().AsDuration())
		listedState.Uptime = nil
	}
	for _, total := range response.GetCallTotals() {
		total.Since = nil
	}
	return response, uptimes
}

// withoutUptime returns a copy of state without its uptime, as list returns it.
func withoutUptime(state *pb.State) *pb.State {
	stripped := proto.CloneOf(state)
	stripped.Uptime = nil
	return stripped
}

func TestUpdateKeepsLatestState(t *testing.T) {
	rpc := newRPC(t)
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	quarantine := &pb.Quarantine{
		Direction:   pb.Direction_DIRECTION_INGRESS,
		Since:       timestamppb.New(since),
		Kind:        pb.Quarantine_KIND_DIVERGENT,
		Traffic:     &pb.Traffic{Direction: pb.Direction_DIRECTION_INGRESS, Protocol: "http", Route: "GET /v1/users"},
		Node:        proto.String("users"),
		Differences: []*pb.Difference{{Path: "$.name", Change: pb.Difference_CHANGE_CHANGED, Count: 1}},
	}
	staging := state("staging", time.Minute)
	calls := &pb.Calls{Routes: map[string]uint64{"GET /v1/users": 22}}
	assert.NoError(t, update(t, rpc, whole(state("production", time.Hour)), keepalive(), quarantined(quarantine), called(&pb.Calls{Other: 1}), called(calls), keepalive()))
	assert.NoError(t, update(t, rpc, whole(staging)))

	// The calls arrived after the quarantine, so they count for nothing.
	production := withoutUptime(state("production", time.Hour, quarantine))
	productionCalls := &pb.CallTotal{Service: "users", Environment: "production", Start: pb.CallTotal_START_FIRST_REPORT, Calls: &pb.Calls{}}
	stagingCalls := &pb.CallTotal{Service: "users", Environment: "staging", Start: pb.CallTotal_START_FIRST_REPORT, Calls: &pb.Calls{}}
	divergence := &pb.Divergence{Service: "users", Environment: "production", Revision: "abc123", Quarantine: quarantine}
	tests := []struct {
		name     string
		request  *pb.ListDivergencesRequest
		expected *pb.ListDivergencesResponse
	}{
		{name: "All", request: &pb.ListDivergencesRequest{}, expected: &pb.ListDivergencesResponse{States: []*pb.State{production, withoutUptime(staging)}, Divergences: []*pb.Divergence{divergence}, CallTotals: []*pb.CallTotal{productionCalls, stagingCalls}}},
		{name: "Environment", request: &pb.ListDivergencesRequest{Environment: proto.String("staging")}, expected: &pb.ListDivergencesResponse{States: []*pb.State{withoutUptime(staging)}, CallTotals: []*pb.CallTotal{stagingCalls}}},
		{name: "Node", request: &pb.ListDivergencesRequest{Node: proto.String("users")}, expected: &pb.ListDivergencesResponse{States: []*pb.State{production}, Divergences: []*pb.Divergence{divergence}, CallTotals: []*pb.CallTotal{productionCalls}}},
		{name: "Revision", request: &pb.ListDivergencesRequest{Revision: proto.String("def456")}, expected: &pb.ListDivergencesResponse{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listed, _ := list(t, rpc, test.request)
			assert.True(t, proto.Equal(test.expected, listed), "%v", listed)
		})
	}

	// Later updates advance the uptime the mirror sent with its whole state.
	_, uptimes := list(t, rpc, &pb.ListDivergencesRequest{})
	assert.True(t, uptimes[0] > time.Hour && uptimes[0] < time.Hour+time.Minute, "%v", uptimes[0])
	assert.Equal(t, time.Minute, uptimes[1])
}

func TestDivergencesOutliveRestartUntilCleared(t *testing.T) {
	rpc := newRPC(t)
	quarantine := &pb.Quarantine{Direction: pb.Direction_DIRECTION_EGRESS, Since: timestamppb.Now(), Kind: pb.Quarantine_KIND_UNMATCHED}
	redeployed := state("production", time.Second)
	redeployed.Revision = "def456"
	assert.NoError(t, update(t, rpc, whole(state("production", time.Minute)), quarantined(quarantine)))
	assert.NoError(t, update(t, rpc, whole(redeployed)))
	divergence := &pb.Divergence{Service: "users", Environment: "production", Revision: "abc123", Quarantine: quarantine}
	firstReport := &pb.CallTotal{Service: "users", Environment: "production", Start: pb.CallTotal_START_FIRST_REPORT, Calls: &pb.Calls{}}
	expected := &pb.ListDivergencesResponse{States: []*pb.State{withoutUptime(redeployed)}, Divergences: []*pb.Divergence{divergence}, CallTotals: []*pb.CallTotal{firstReport}}
	listed, _ := list(t, rpc, &pb.ListDivergencesRequest{})
	assert.True(t, proto.Equal(expected, listed), "%v", listed)

	_, err := rpc.ClearDivergences(t.Context(), &pb.ClearDivergencesRequest{Service: "users", Environment: "production"})
	assert.NoError(t, err)
	// A mirror still running reports the cleared quarantine again.
	assert.NoError(t, update(t, rpc, whole(state("production", time.Minute, quarantine))))
	assert.NoError(t, update(t, rpc, whole(redeployed)))
	listed, _ = list(t, rpc, &pb.ListDivergencesRequest{})
	cleared := &pb.CallTotal{Service: "users", Environment: "production", Start: pb.CallTotal_START_CLEAR, Calls: &pb.Calls{}}
	assert.True(t, proto.Equal(&pb.ListDivergencesResponse{States: []*pb.State{withoutUptime(redeployed)}, CallTotals: []*pb.CallTotal{cleared}}, listed), "%v", listed)

	_, err = rpc.ClearDivergences(t.Context(), &pb.ClearDivergencesRequest{Service: "users"})
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCallTotalsOutliveRedeploysUntilQuarantine(t *testing.T) {
	rpc := newRPC(t)
	routes := func(count uint64) *pb.Calls { return &pb.Calls{Routes: map[string]uint64{"GET /v1/users": count}} }
	run := func(id string, quarantines ...*pb.Quarantine) *pb.State {
		reported := state("production", time.Minute, quarantines...)
		reported.Run = id
		return reported
	}
	listTotal := func(t *testing.T) (*pb.State, *pb.CallTotal) {
		t.Helper()
		listed, _ := list(t, rpc, &pb.ListDivergencesRequest{})
		assert.Equal(t, 1, len(listed.GetCallTotals()))
		return listed.GetStates()[0], listed.GetCallTotals()[0]
	}
	total := func(start pb.CallTotal_Start, calls *pb.Calls, runs uint32) *pb.CallTotal {
		return &pb.CallTotal{Service: "users", Environment: "production", Start: start, Calls: calls, Runs: runs}
	}

	assert.NoError(t, update(t, rpc, whole(run("a")), called(routes(3)), called(routes(5))))
	assert.NoError(t, update(t, rpc, whole(run("b")), called(routes(1))))
	listed, counted := listTotal(t)
	assert.True(t, proto.Equal(total(pb.CallTotal_START_FIRST_REPORT, routes(6), 2), counted), "%v", counted)
	assert.True(t, proto.Equal(routes(1), listed.GetCalls()), "%v", listed)

	// A quarantine restarts the total, and the quarantined run adds nothing more.
	quarantine := &pb.Quarantine{Direction: pb.Direction_DIRECTION_INGRESS, Since: timestamppb.Now(), Kind: pb.Quarantine_KIND_UNMATCHED}
	assert.NoError(t, update(t, rpc, whole(run("b")), quarantined(quarantine), called(routes(4))))
	listed, counted = listTotal(t)
	assert.True(t, proto.Equal(total(pb.CallTotal_START_QUARANTINE, &pb.Calls{}, 0), counted), "%v", counted)
	assert.Equal(t, nil, listed.GetCalls())

	assert.NoError(t, update(t, rpc, whole(run("c")), called(routes(2))))
	_, counted = listTotal(t)
	assert.True(t, proto.Equal(total(pb.CallTotal_START_QUARANTINE, routes(2), 1), counted), "%v", counted)

	_, err := rpc.ClearDivergences(t.Context(), &pb.ClearDivergencesRequest{Service: "users", Environment: "production"})
	assert.NoError(t, err)
	assert.NoError(t, update(t, rpc, whole(run("c")), called(routes(3))))
	listed, counted = listTotal(t)
	assert.True(t, proto.Equal(total(pb.CallTotal_START_CLEAR, routes(1), 1), counted), "%v", counted)
	assert.True(t, proto.Equal(routes(1), listed.GetCalls()), "%v", listed)
}

func TestUpdateRejectsInvalidStream(t *testing.T) {
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
		name     string
		requests []*pb.UpdateRequest
	}{
		{name: "Empty", requests: []*pb.UpdateRequest{{}}},
		{name: "WithoutState", requests: []*pb.UpdateRequest{whole(nil)}},
		{name: "BeginsWithKeepalive", requests: []*pb.UpdateRequest{keepalive()}},
		{name: "BeginsWithQuarantine", requests: []*pb.UpdateRequest{quarantined(quarantine(pb.Direction_DIRECTION_INGRESS))}},
		{name: "BeginsWithCalls", requests: []*pb.UpdateRequest{called(&pb.Calls{Other: 1})}},
		{name: "CallsWithoutRoute", requests: []*pb.UpdateRequest{whole(state("production", time.Minute)), called(&pb.Calls{Routes: map[string]uint64{"": 1}})}},
		{name: "WithoutRevision", requests: []*pb.UpdateRequest{whole(withoutCommit)}},
		{name: "WithoutUptime", requests: []*pb.UpdateRequest{whole(withoutUptime)}},
		{name: "UnspecifiedQuarantineDirection", requests: []*pb.UpdateRequest{whole(state("production", time.Minute, quarantine(pb.Direction_DIRECTION_UNSPECIFIED)))}},
		{name: "RepeatedDirection", requests: []*pb.UpdateRequest{whole(state("production", time.Minute, quarantine(pb.Direction_DIRECTION_INGRESS), quarantine(pb.Direction_DIRECTION_INGRESS)))}},
		{name: "UnspecifiedKind", requests: []*pb.UpdateRequest{whole(withoutKind)}},
		{name: "UnspecifiedTrafficDirection", requests: []*pb.UpdateRequest{whole(withoutTrafficDirection)}},
		{name: "InvalidQuarantine", requests: []*pb.UpdateRequest{whole(state("production", time.Minute)), quarantined(quarantine(pb.Direction_DIRECTION_UNSPECIFIED))}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := update(t, rpc, test.requests...)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestUpdateEndsRejectedStreamWhileMirrorSends(t *testing.T) {
	rpc := newRPC(t)
	stream, err := rpc.Update(t.Context())
	assert.NoError(t, err)
	assert.NoError(t, stream.Send(whole(state("production", time.Minute))))
	assert.NoError(t, stream.Send(quarantined(&pb.Quarantine{Since: timestamppb.Now(), Kind: pb.Quarantine_KIND_UNMATCHED})))
	// A mirror only learns that the server ended its stream when a send fails.
	deadline := time.Now().Add(10 * time.Second)
	for err == nil && time.Now().Before(deadline) {
		err = stream.Send(keepalive())
		time.Sleep(time.Millisecond)
	}
	assert.IsError(t, err, io.EOF)
	_, err = stream.CloseAndReceive()
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

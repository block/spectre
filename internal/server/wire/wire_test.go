package wire_test

import (
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/wire"
	pb "github.com/block/spectre/spectrepb"
)

func TestStateRoundTrip(t *testing.T) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		state model.State
	}{
		{name: "Healthy", state: model.State{
			Service:         "users",
			Environment:     "production",
			CandidateCommit: "abc123",
			SpectreVersion:  "v0.47.0",
			Uptime:          3 * time.Hour,
		}},
		{name: "Quarantined", state: model.State{
			Service:         "users",
			Environment:     "staging",
			CandidateCommit: "abc123",
			SpectreVersion:  "v0.47.0",
			Uptime:          time.Minute,
			Quarantines: map[model.Direction]model.Quarantine{
				model.Ingress: {Since: since, Reason: model.QuarantineReason{
					Kind:        model.QuarantineDivergent,
					Traffic:     Some(model.Traffic{Direction: model.Ingress, Protocol: model.ProtocolHTTP, Route: "GET /v1/users"}),
					Node:        Some("users"),
					Differences: []string{"body.name"},
				}},
				model.Egress: {Since: since, Reason: model.QuarantineReason{
					Kind:   model.QuarantineCapacity,
					Detail: "candidate concurrency limit exceeded",
				}},
			},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			restored, err := wire.StateFromMessage(wire.StateMessage(test.state))
			assert.NoError(t, err)
			assert.Equal(t, test.state, restored)
		})
	}
}

func TestStateFromMessageRejectsInvalid(t *testing.T) {
	quarantine := func(direction pb.Direction) *pb.Quarantine {
		return &pb.Quarantine{Direction: direction, Since: timestamppb.Now(), Kind: pb.Quarantine_KIND_UNMATCHED}
	}
	valid := func() *pb.State {
		return &pb.State{Service: "users", Environment: "production", CandidateCommit: "abc123", Uptime: durationpb.New(time.Minute)}
	}
	withoutCommit := valid()
	withoutCommit.CandidateCommit = ""
	withoutUptime := valid()
	withoutUptime.Uptime = nil
	withoutDirection := valid()
	withoutDirection.Quarantines = []*pb.Quarantine{quarantine(pb.Direction_DIRECTION_UNSPECIFIED)}
	duplicate := valid()
	duplicate.Quarantines = []*pb.Quarantine{quarantine(pb.Direction_DIRECTION_INGRESS), quarantine(pb.Direction_DIRECTION_INGRESS)}
	withoutSince := valid()
	withoutSince.Quarantines = []*pb.Quarantine{{Direction: pb.Direction_DIRECTION_INGRESS, Kind: pb.Quarantine_KIND_UNMATCHED}}
	withoutKind := valid()
	withoutKind.Quarantines = []*pb.Quarantine{{Direction: pb.Direction_DIRECTION_INGRESS, Since: timestamppb.Now()}}
	tests := []struct {
		name  string
		state *pb.State
		err   string
	}{
		{name: "WithoutCandidateCommit", state: withoutCommit, err: "state needs a service, environment, and candidate commit"},
		{name: "WithoutUptime", state: withoutUptime, err: "state needs an uptime"},
		{name: "UnspecifiedDirection", state: withoutDirection, err: "quarantine: unknown direction DIRECTION_UNSPECIFIED"},
		{name: "DuplicateDirection", state: duplicate, err: "state holds more than one ingress quarantine"},
		{name: "WithoutSince", state: withoutSince, err: "quarantine: quarantine needs its start time"},
		{name: "UnspecifiedKind", state: withoutKind, err: "quarantine: unknown quarantine kind KIND_UNSPECIFIED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := wire.StateFromMessage(test.state)
			assert.EqualError(t, err, test.err)
		})
	}
}

func TestFilterRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		filter model.StateFilter
	}{
		{name: "Unset"},
		{name: "Set", filter: model.StateFilter{Service: Some("users"), Environment: Some("production"), CandidateCommit: Some("abc123"), Node: Some("users")}},
		// An empty filter value is set, and matches only empty values.
		{name: "Empty", filter: model.StateFilter{Node: Some("")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.filter, wire.FilterFromMessage(wire.FilterMessage(test.filter)))
		})
	}
}

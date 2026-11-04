// Package wire converts between the SPECTRE service protocol and the domain
// model, so only the server's API boundary handles protocol messages.
package wire

import (
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/model"
	pb "github.com/block/spectre/spectrepb"
)

// StateMessage returns the message that reports state.
func StateMessage(state model.State) *pb.State {
	message := &pb.State{
		Service:         state.Service,
		Environment:     state.Environment,
		CandidateCommit: state.CandidateCommit,
		SpectreVersion:  state.SpectreVersion,
		Uptime:          durationpb.New(state.Uptime),
	}
	// Ingress first, so a state always encodes the same way.
	for _, direction := range []model.Direction{model.Ingress, model.Egress} {
		if quarantine, ok := state.Quarantines[direction]; ok {
			message.Quarantines = append(message.Quarantines, quarantineMessage(direction, quarantine))
		}
	}
	return message
}

// StateFromMessage restores a reported state, checking that it identifies its
// mirror and holds at most one quarantine per direction.
func StateFromMessage(message *pb.State) (model.State, error) {
	if message.GetService() == "" || message.GetEnvironment() == "" || message.GetCandidateCommit() == "" {
		return model.State{}, errors.New("state needs a service, environment, and candidate commit")
	}
	if message.GetUptime() == nil {
		return model.State{}, errors.New("state needs an uptime")
	}
	state := model.State{
		Service:         message.GetService(),
		Environment:     message.GetEnvironment(),
		CandidateCommit: message.GetCandidateCommit(),
		SpectreVersion:  message.GetSpectreVersion(),
		Uptime:          message.GetUptime().AsDuration(),
	}
	quarantines := message.GetQuarantines()
	if len(quarantines) > 0 {
		state.Quarantines = make(map[model.Direction]model.Quarantine, len(quarantines))
	}
	for _, quarantine := range quarantines {
		direction, err := directionFromMessage(quarantine.GetDirection())
		if err != nil {
			return model.State{}, errors.Wrap(err, "quarantine")
		}
		if _, ok := state.Quarantines[direction]; ok {
			return model.State{}, errors.Errorf("state holds more than one %s quarantine", direction)
		}
		restored, err := quarantineFromMessage(quarantine)
		if err != nil {
			return model.State{}, errors.Wrap(err, "quarantine")
		}
		state.Quarantines[direction] = restored
	}
	return state, nil
}

// StatesJSON returns states as the protocol's JSON list response, which is
// what the command line prints.
func StatesJSON(states []model.State) ([]byte, error) {
	response := &pb.ListDivergencesResponse{States: make([]*pb.State, 0, len(states))}
	for _, state := range states {
		response.States = append(response.States, StateMessage(state))
	}
	data, err := protojson.MarshalOptions{Multiline: true}.Marshal(response)
	return data, errors.Wrap(err, "encode JSON")
}

// FilterMessage returns the request that lists the states filter selects.
func FilterMessage(filter model.StateFilter) *pb.ListDivergencesRequest {
	return &pb.ListDivergencesRequest{
		Service:         filter.Service.Ptr(),
		Environment:     filter.Environment.Ptr(),
		CandidateCommit: filter.CandidateCommit.Ptr(),
		Node:            filter.Node.Ptr(),
	}
}

// FilterFromMessage restores the filter of a list request.
func FilterFromMessage(request *pb.ListDivergencesRequest) model.StateFilter {
	// Getters hide whether an optional field is set, so presence is a nil check.
	return model.StateFilter{
		Service:         From(request.GetService(), request.Service != nil),
		Environment:     From(request.GetEnvironment(), request.Environment != nil),
		CandidateCommit: From(request.GetCandidateCommit(), request.CandidateCommit != nil),
		Node:            From(request.GetNode(), request.Node != nil),
	}
}

func quarantineMessage(direction model.Direction, status model.Quarantine) *pb.Quarantine {
	reason := status.Reason
	message := &pb.Quarantine{
		Direction:   directionMessage(direction),
		Since:       timestamppb.New(status.Since),
		Kind:        kindMessage(reason.Kind),
		Differences: reason.Differences,
		Detail:      reason.Detail,
		Node:        reason.Node.Ptr(),
	}
	if traffic, ok := reason.Traffic.Get(); ok {
		message.Traffic = &pb.Traffic{
			Direction: directionMessage(traffic.Direction),
			Protocol:  string(traffic.Protocol),
			Route:     string(traffic.Route),
		}
	}
	return message
}

// quarantineFromMessage restores a reported quarantine, which must say when it began.
func quarantineFromMessage(message *pb.Quarantine) (model.Quarantine, error) {
	if message.GetSince() == nil {
		return model.Quarantine{}, errors.New("quarantine needs its start time")
	}
	reason, err := reasonFromMessage(message)
	if err != nil {
		return model.Quarantine{}, err
	}
	return model.Quarantine{Reason: reason, Since: message.GetSince().AsTime()}, nil
}

// reasonFromMessage restores the reason a mirror reported. Protobuf getters
// return plain strings, so the wire format is where named types are restored.
func reasonFromMessage(message *pb.Quarantine) (model.QuarantineReason, error) {
	kind, err := kindFromMessage(message.GetKind())
	if err != nil {
		return model.QuarantineReason{}, err
	}
	reason := model.QuarantineReason{
		Kind:        kind,
		Node:        Zero(message.GetNode()),
		Differences: message.GetDifferences(),
		Detail:      message.GetDetail(),
	}
	if traffic := message.GetTraffic(); traffic != nil {
		direction, err := directionFromMessage(traffic.GetDirection())
		if err != nil {
			return model.QuarantineReason{}, err
		}
		reason.Traffic = Some(model.Traffic{
			Direction: direction,
			Protocol:  model.Protocol(traffic.GetProtocol()), //nolint:typeboundary // protobuf getters return plain strings
			Route:     model.Route(traffic.GetRoute()),       //nolint:typeboundary // protobuf getters return plain strings
		})
	}
	return reason, nil
}
func kindMessage(kind model.QuarantineKind) pb.Quarantine_Kind {
	switch kind {
	case model.QuarantineDivergent:
		return pb.Quarantine_KIND_DIVERGENT
	case model.QuarantineUnable:
		return pb.Quarantine_KIND_UNABLE
	case model.QuarantineUnmatched:
		return pb.Quarantine_KIND_UNMATCHED
	case model.QuarantineUnlisted:
		return pb.Quarantine_KIND_UNLISTED
	case model.QuarantineCapacity:
		return pb.Quarantine_KIND_CAPACITY
	case model.QuarantinePairing:
		return pb.Quarantine_KIND_PAIRING
	}
	return pb.Quarantine_KIND_UNSPECIFIED
}

func kindFromMessage(kind pb.Quarantine_Kind) (model.QuarantineKind, error) {
	switch kind {
	case pb.Quarantine_KIND_DIVERGENT:
		return model.QuarantineDivergent, nil
	case pb.Quarantine_KIND_UNABLE:
		return model.QuarantineUnable, nil
	case pb.Quarantine_KIND_UNMATCHED:
		return model.QuarantineUnmatched, nil
	case pb.Quarantine_KIND_UNLISTED:
		return model.QuarantineUnlisted, nil
	case pb.Quarantine_KIND_CAPACITY:
		return model.QuarantineCapacity, nil
	case pb.Quarantine_KIND_PAIRING:
		return model.QuarantinePairing, nil
	case pb.Quarantine_KIND_UNSPECIFIED:
	}
	return "", errors.Errorf("unknown quarantine kind %s", kind)
}

func directionMessage(direction model.Direction) pb.Direction {
	switch direction {
	case model.Ingress:
		return pb.Direction_DIRECTION_INGRESS
	case model.Egress:
		return pb.Direction_DIRECTION_EGRESS
	}
	return pb.Direction_DIRECTION_UNSPECIFIED
}

// directionFromMessage restores the direction a mirror reported.
func directionFromMessage(direction pb.Direction) (model.Direction, error) {
	switch direction {
	case pb.Direction_DIRECTION_INGRESS:
		return model.Ingress, nil
	case pb.Direction_DIRECTION_EGRESS:
		return model.Egress, nil
	case pb.Direction_DIRECTION_UNSPECIFIED:
	}
	return "", errors.Errorf("unknown direction %s", direction)
}

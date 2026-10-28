// Package wire converts between the SPECTRE service protocol and the domain
// model, so only the server's API boundary handles protocol messages.
package wire

import (
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/model"
	pb "github.com/block/spectre/spectrepb"
)

// DivergenceMessage returns the message that reports divergence.
func DivergenceMessage(divergence model.Divergence) *pb.Divergence {
	return &pb.Divergence{
		Service:     divergence.Service,
		Environment: divergence.Environment,
		Revision:    divergence.Revision,
		Direction:   directionMessage(divergence.Direction),
		Quarantine:  quarantineMessage(divergence.Quarantine),
	}
}

// DivergenceFromMessage restores a reported divergence, checking that it
// identifies its source and quarantine so every store can rely on them.
func DivergenceFromMessage(message *pb.Divergence) (model.Divergence, error) {
	if message.GetService() == "" || message.GetEnvironment() == "" || message.GetRevision() == "" {
		return model.Divergence{}, errors.New("divergence needs a service, environment, and revision")
	}
	direction, err := directionFromMessage(message.GetDirection())
	if err != nil {
		return model.Divergence{}, err
	}
	quarantine := message.GetQuarantine()
	if quarantine.GetSince() == nil {
		return model.Divergence{}, errors.New("divergence needs a quarantine with its start time")
	}
	reason, err := reasonFromMessage(quarantine)
	if err != nil {
		return model.Divergence{}, errors.Wrap(err, "quarantine")
	}
	return model.Divergence{
		Service:     message.GetService(),
		Environment: message.GetEnvironment(),
		Revision:    message.GetRevision(),
		Direction:   direction,
		Quarantine:  model.Quarantine{Reason: reason, Since: quarantine.GetSince().AsTime()},
	}, nil
}

// DivergencesJSON returns divergences as the protocol's JSON list response,
// which is what the command line prints.
func DivergencesJSON(divergences []model.Divergence) ([]byte, error) {
	response := &pb.ListDivergencesResponse{Divergences: make([]*pb.Divergence, 0, len(divergences))}
	for _, divergence := range divergences {
		response.Divergences = append(response.Divergences, DivergenceMessage(divergence))
	}
	data, err := protojson.MarshalOptions{Multiline: true}.Marshal(response)
	return data, errors.Wrap(err, "encode JSON")
}

// FilterMessage returns the request that lists the divergences filter selects.
func FilterMessage(filter model.DivergenceFilter) *pb.ListDivergencesRequest {
	return &pb.ListDivergencesRequest{
		Service:     filter.Service.Ptr(),
		Environment: filter.Environment.Ptr(),
		Revision:    filter.Revision.Ptr(),
		Node:        filter.Node.Ptr(),
	}
}

// FilterFromMessage restores the filter of a list request.
func FilterFromMessage(request *pb.ListDivergencesRequest) model.DivergenceFilter {
	// Getters hide whether an optional field is set, so presence is a nil check.
	return model.DivergenceFilter{
		Service:     From(request.GetService(), request.Service != nil),
		Environment: From(request.GetEnvironment(), request.Environment != nil),
		Revision:    From(request.GetRevision(), request.Revision != nil),
		Node:        From(request.GetNode(), request.Node != nil),
	}
}

func quarantineMessage(status model.Quarantine) *pb.Quarantine {
	reason := status.Reason
	message := &pb.Quarantine{
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

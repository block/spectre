package server

import (
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/model"
	pb "github.com/block/spectre/spectrepb"
)

func quarantineMessage(status model.Quarantine) *pb.Quarantine {
	reason := status.Reason
	message := &pb.Quarantine{
		Since:       timestamppb.New(status.Since),
		Kind:        kindMessage(reason.Kind),
		Differences: reason.Differences,
		Detail:      reason.Detail,
	}
	if traffic, ok := reason.Traffic.Get(); ok {
		message.Traffic = &pb.Traffic{
			Direction: directionMessage(traffic.Direction),
			Protocol:  string(traffic.Protocol),
			Route:     string(traffic.Route),
		}
	}
	if node, ok := reason.Node.Get(); ok {
		message.Node = &node
	}
	return message
}

// ReasonFromMessage restores the reason a mirror reported. Protobuf getters
// return plain strings, so the wire format is where named types are restored.
func ReasonFromMessage(message *pb.Quarantine) (model.QuarantineReason, error) {
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
		direction, err := DirectionFromMessage(traffic.GetDirection())
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

// DirectionFromMessage restores the direction a mirror reported.
func DirectionFromMessage(direction pb.Direction) (model.Direction, error) {
	switch direction {
	case pb.Direction_DIRECTION_INGRESS:
		return model.Ingress, nil
	case pb.Direction_DIRECTION_EGRESS:
		return model.Egress, nil
	case pb.Direction_DIRECTION_UNSPECIFIED:
	}
	return "", errors.Errorf("unknown direction %s", direction)
}

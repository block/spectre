package server

import (
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/quarantine"
	pb "github.com/block/spectre/spectrepb"
)

func quarantineMessage(status proxy.QuarantineStatus) *pb.Quarantine {
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
func ReasonFromMessage(message *pb.Quarantine) (quarantine.Reason, error) {
	kind, err := kindFromMessage(message.GetKind())
	if err != nil {
		return quarantine.Reason{}, err
	}
	reason := quarantine.Reason{
		Kind:        kind,
		Node:        Zero(message.GetNode()),
		Differences: message.GetDifferences(),
		Detail:      message.GetDetail(),
	}
	if traffic := message.GetTraffic(); traffic != nil {
		direction, err := DirectionFromMessage(traffic.GetDirection())
		if err != nil {
			return quarantine.Reason{}, err
		}
		reason.Traffic = Some(quarantine.Traffic{
			Direction: direction,
			Protocol:  boundary.Protocol(traffic.GetProtocol()), //nolint:typeboundary // protobuf getters return plain strings
			Route:     boundary.Route(traffic.GetRoute()),       //nolint:typeboundary // protobuf getters return plain strings
		})
	}
	return reason, nil
}

func kindMessage(kind quarantine.Kind) pb.Quarantine_Kind {
	switch kind {
	case quarantine.KindDivergent:
		return pb.Quarantine_KIND_DIVERGENT
	case quarantine.KindUnable:
		return pb.Quarantine_KIND_UNABLE
	case quarantine.KindUnmatched:
		return pb.Quarantine_KIND_UNMATCHED
	case quarantine.KindUnlisted:
		return pb.Quarantine_KIND_UNLISTED
	case quarantine.KindCapacity:
		return pb.Quarantine_KIND_CAPACITY
	case quarantine.KindPairing:
		return pb.Quarantine_KIND_PAIRING
	}
	return pb.Quarantine_KIND_UNSPECIFIED
}

func kindFromMessage(kind pb.Quarantine_Kind) (quarantine.Kind, error) {
	switch kind {
	case pb.Quarantine_KIND_DIVERGENT:
		return quarantine.KindDivergent, nil
	case pb.Quarantine_KIND_UNABLE:
		return quarantine.KindUnable, nil
	case pb.Quarantine_KIND_UNMATCHED:
		return quarantine.KindUnmatched, nil
	case pb.Quarantine_KIND_UNLISTED:
		return quarantine.KindUnlisted, nil
	case pb.Quarantine_KIND_CAPACITY:
		return quarantine.KindCapacity, nil
	case pb.Quarantine_KIND_PAIRING:
		return quarantine.KindPairing, nil
	case pb.Quarantine_KIND_UNSPECIFIED:
	}
	return "", errors.Errorf("unknown quarantine kind %s", kind)
}

func directionMessage(direction boundary.Direction) pb.Direction {
	switch direction {
	case boundary.Ingress:
		return pb.Direction_DIRECTION_INGRESS
	case boundary.Egress:
		return pb.Direction_DIRECTION_EGRESS
	}
	return pb.Direction_DIRECTION_UNSPECIFIED
}

// DirectionFromMessage restores the direction a mirror reported.
func DirectionFromMessage(direction pb.Direction) (boundary.Direction, error) {
	switch direction {
	case pb.Direction_DIRECTION_INGRESS:
		return boundary.Ingress, nil
	case pb.Direction_DIRECTION_EGRESS:
		return boundary.Egress, nil
	case pb.Direction_DIRECTION_UNSPECIFIED:
	}
	return "", errors.Errorf("unknown direction %s", direction)
}

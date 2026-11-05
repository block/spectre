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
		Service:        state.Service,
		Environment:    state.Environment,
		Revision:       state.Revision,
		SpectreVersion: state.SpectreVersion,
		Uptime:         durationpb.New(state.Uptime),
	}
	if len(state.Calls.Routes) > 0 || state.Calls.Other > 0 {
		message.Calls = CallsMessage(state.Calls)
	}
	// Ingress first, so a state always encodes the same way.
	for _, direction := range []model.Direction{model.Ingress, model.Egress} {
		if quarantine, ok := state.Quarantines[direction]; ok {
			message.Quarantines = append(message.Quarantines, QuarantineMessage(direction, quarantine))
		}
	}
	return message
}

// StateFromMessage restores a reported state, checking that it identifies its
// mirror and holds at most one quarantine per direction.
func StateFromMessage(message *pb.State) (model.State, error) {
	if message.GetService() == "" || message.GetEnvironment() == "" || message.GetRevision() == "" {
		return model.State{}, errors.New("state needs a service, environment, and revision")
	}
	if message.GetUptime() == nil {
		return model.State{}, errors.New("state needs an uptime")
	}
	state := model.State{
		Service:        message.GetService(),
		Environment:    message.GetEnvironment(),
		Revision:       message.GetRevision(),
		SpectreVersion: message.GetSpectreVersion(),
		Uptime:         message.GetUptime().AsDuration(),
	}
	calls, err := CallsFromMessage(message.GetCalls())
	if err != nil {
		return model.State{}, err
	}
	state.Calls = calls
	quarantines := message.GetQuarantines()
	if len(quarantines) > 0 {
		state.Quarantines = make(map[model.Direction]model.Quarantine, len(quarantines))
	}
	for _, quarantine := range quarantines {
		direction, restored, err := QuarantineFromMessage(quarantine)
		if err != nil {
			return model.State{}, err
		}
		if _, ok := state.Quarantines[direction]; ok {
			return model.State{}, errors.Errorf("state holds more than one %s quarantine", direction)
		}
		state.Quarantines[direction] = restored
	}
	return state, nil
}

// UpdateMessage returns the message that streams update.
func UpdateMessage(update model.Update) *pb.UpdateRequest {
	switch update := update.(type) {
	case model.State:
		return &pb.UpdateRequest{Update: &pb.UpdateRequest_State{State: StateMessage(update)}}
	case model.QuarantineUpdate:
		return &pb.UpdateRequest{Update: &pb.UpdateRequest_Quarantine{Quarantine: QuarantineMessage(update.Direction, update.Quarantine)}}
	case model.Calls:
		return &pb.UpdateRequest{Update: &pb.UpdateRequest_Calls{Calls: CallsMessage(update)}}
	}
	return &pb.UpdateRequest{}
}

// UpdateFromMessage restores a streamed update. A keepalive holds none.
func UpdateFromMessage(message *pb.UpdateRequest) (model.Update, error) {
	switch update := message.GetUpdate().(type) {
	case *pb.UpdateRequest_State:
		return StateFromMessage(update.State)
	case *pb.UpdateRequest_Quarantine:
		direction, quarantine, err := QuarantineFromMessage(update.Quarantine)
		if err != nil {
			return nil, err
		}
		return model.QuarantineUpdate{Direction: direction, Quarantine: quarantine}, nil
	case *pb.UpdateRequest_Calls:
		return CallsFromMessage(update.Calls)
	case *pb.UpdateRequest_Keepalive:
		return nil, errors.New("a keepalive holds no update")
	}
	return nil, errors.New("update holds nothing")
}

// CallsMessage returns the message that reports call counts.
func CallsMessage(calls model.Calls) *pb.Calls {
	message := &pb.Calls{Other: calls.Other}
	if len(calls.Routes) > 0 {
		message.Routes = make(map[string]uint64, len(calls.Routes))
	}
	for route, count := range calls.Routes {
		message.Routes[string(route)] = count
	}
	return message
}

// CallsFromMessage restores reported call counts, checking that each names
// its route. An absent message counts no calls.
func CallsFromMessage(message *pb.Calls) (model.Calls, error) {
	calls := model.Calls{Other: message.GetOther()}
	routes := message.GetRoutes()
	if len(routes) > 0 {
		calls.Routes = make(map[model.Route]uint64, len(routes))
	}
	for route, count := range routes {
		if route == "" {
			return model.Calls{}, errors.New("call counts need a route")
		}
		calls.Routes[model.Route(route)] = count //nolint:typeboundary // protobuf map keys are plain strings
	}
	return calls, nil
}

// DivergenceMessage returns the message that reports divergence.
func DivergenceMessage(divergence model.Divergence) *pb.Divergence {
	return &pb.Divergence{
		Service:     divergence.Service,
		Environment: divergence.Environment,
		Revision:    divergence.Revision,
		Quarantine:  QuarantineMessage(divergence.Direction, divergence.Quarantine),
	}
}

// DivergenceFromMessage restores a listed divergence, checking that it
// identifies its mirror and direction.
func DivergenceFromMessage(message *pb.Divergence) (model.Divergence, error) {
	if message.GetService() == "" || message.GetEnvironment() == "" || message.GetRevision() == "" {
		return model.Divergence{}, errors.New("divergence needs a service, environment, and revision")
	}
	if message.GetQuarantine() == nil {
		return model.Divergence{}, errors.New("divergence needs a quarantine")
	}
	direction, quarantine, err := QuarantineFromMessage(message.GetQuarantine())
	if err != nil {
		return model.Divergence{}, err
	}
	return model.Divergence{
		Service:     message.GetService(),
		Environment: message.GetEnvironment(),
		Revision:    message.GetRevision(),
		Direction:   direction,
		Quarantine:  quarantine,
	}, nil
}

// ListResponse returns the protocol's list response for states and divergences.
func ListResponse(states []model.State, divergences []model.Divergence) *pb.ListDivergencesResponse {
	response := &pb.ListDivergencesResponse{
		States:      make([]*pb.State, 0, len(states)),
		Divergences: make([]*pb.Divergence, 0, len(divergences)),
	}
	for _, state := range states {
		response.States = append(response.States, StateMessage(state))
	}
	for _, divergence := range divergences {
		response.Divergences = append(response.Divergences, DivergenceMessage(divergence))
	}
	return response
}

// ListFromResponse restores the states and divergences of a list response.
func ListFromResponse(response *pb.ListDivergencesResponse) ([]model.State, []model.Divergence, error) {
	states := make([]model.State, 0, len(response.GetStates()))
	for _, message := range response.GetStates() {
		state, err := StateFromMessage(message)
		if err != nil {
			return nil, nil, err
		}
		states = append(states, state)
	}
	divergences := make([]model.Divergence, 0, len(response.GetDivergences()))
	for _, message := range response.GetDivergences() {
		divergence, err := DivergenceFromMessage(message)
		if err != nil {
			return nil, nil, err
		}
		divergences = append(divergences, divergence)
	}
	return states, divergences, nil
}

// ListJSON returns states and divergences as the protocol's JSON list
// response, which is what the command line prints.
func ListJSON(states []model.State, divergences []model.Divergence) ([]byte, error) {
	data, err := protojson.MarshalOptions{Multiline: true}.Marshal(ListResponse(states, divergences))
	return data, errors.Wrap(err, "encode JSON")
}

// FilterMessage returns the request that lists what filter selects.
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

// QuarantineMessage returns the message that reports the quarantine of the
// proxy in direction.
func QuarantineMessage(direction model.Direction, status model.Quarantine) *pb.Quarantine {
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

// QuarantineFromMessage restores a reported quarantine and the direction of
// the proxy that holds it.
func QuarantineFromMessage(message *pb.Quarantine) (model.Direction, model.Quarantine, error) {
	direction, err := directionFromMessage(message.GetDirection())
	if err != nil {
		return "", model.Quarantine{}, errors.Wrap(err, "quarantine")
	}
	quarantine, err := quarantineFromMessage(message)
	if err != nil {
		return "", model.Quarantine{}, errors.Wrap(err, "quarantine")
	}
	return direction, quarantine, nil
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

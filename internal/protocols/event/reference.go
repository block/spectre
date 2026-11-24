package event

import (
	"context"

	"connectrpc.com/connect/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/event/script"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/schema"
	"github.com/block/spectre/spectrepb/eventpb"
	"github.com/block/spectre/spectrepb/eventpb/eventpbconnect"
)

// referenceService receives the reference's events. The reference is authoritative, so
// nothing it reports is refused, and a problem the scripts cause only stops shadowing.
type referenceService struct {
	mapper *mapper
	rules  rules
	// migration maps each ingress[event] type to its node, when the mirror has a state.
	migration Option[map[string]stateType]
	// quarantiner quarantines on ingress, where start events are shadowed.
	quarantiner engine.Quarantiner
	// recorder is egress's reference point, which records produced events.
	recorder engine.Acceptor
	// directions are both directions' candidate admission, either of which may quarantine.
	directions []*proxy.Candidates
	triggers   *triggers
}

func newReferenceService(
	mapper *mapper, rules rules, migration Option[map[string]stateType], quarantiner engine.Quarantiner,
	recorder engine.Acceptor, directions []*proxy.Candidates, triggers *triggers,
) *referenceService {
	return &referenceService{
		mapper: mapper, rules: rules, migration: migration, quarantiner: quarantiner,
		recorder: recorder, directions: directions, triggers: triggers,
	}
}

var _ eventpbconnect.EventServiceHandler = (*referenceService)(nil)

// Report starts a run for an event that starts work, and records any other event in its run.
func (r *referenceService) Report(ctx context.Context, request *eventpb.ReportRequest) (*eventpb.ReportResponse, error) {
	event := request.GetEvent()
	if event == nil || event.GetType() == "" {
		return nil, invalidArgument("report needs an event with a type")
	}
	logger.FromContext(ctx).DebugContext(ctx, "Reference reported an event",
		"event", logger.EventCorrelation, "type", event.GetType(), "attributes", event.GetAttributes())
	if start, starts := r.rules.start(event.GetType()); starts {
		return r.begin(ctx, start, event)
	}
	if request.RunId == nil {
		// The event belongs to no shadowed run, so no candidate event is expected for it.
		return &eventpb.ReportResponse{}, nil
	}
	id, err := typeid.Parse[engine.RequestID](request.GetRunId())
	if err != nil {
		return nil, invalidArgument("run ID is malformed")
	}
	result := None[*eventpb.Result]()
	if request.GetResult() != nil {
		result = Some(request.GetResult())
	}
	if err := r.record(ctx, id, event, result); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &eventpb.ReportResponse{}, nil
}

// Receive is only for the candidate.
func (r *referenceService) Receive(context.Context, *eventpb.ReceiveRequest, eventpbconnect.EventServiceReceiveServerStream) error {
	return connect.NewError(connect.CodePermissionDenied, "only the candidate receives runs")
}

// begin gives a shadowed start event a run ID and queues it for the candidate. An unshadowed
// one gets no run ID, so the reference runs it, and egress forwards its calls, unpaired.
func (r *referenceService) begin(ctx context.Context, start script.Start, event *eventpb.Event) (*eventpb.ReportResponse, error) {
	unshadowed := &eventpb.ReportResponse{}
	if !r.shadows(ctx, start.Type) {
		return unshadowed, nil
	}
	payload := event.GetPayload()
	if mapping, maps := start.Payload.Get(); maps {
		mapped, err := r.mapper.mapJSON(ctx, mapping, payload)
		if err != nil {
			r.quarantiner.Quarantine(ctx, reason(model.QuarantineUnable, model.Ingress, start.Type,
				errors.Wrap(err, "map the candidate's payload").Error()))
			return unshadowed, nil
		}
		payload = mapped
	}
	id, err := typeid.New[engine.RequestID]()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	runID := id.String()
	trigger := &eventpb.ReceiveResponse{
		RunId: runID,
		Event: &eventpb.Event{Type: start.Candidate, Payload: payload, Attributes: event.GetAttributes()},
	}
	if !r.triggers.offer(trigger) {
		logger.FromContext(ctx).InfoContext(ctx, "Run is not shadowed, because no candidate stream can take it",
			"event", logger.EventCorrelation, "type", start.Type)
		return unshadowed, nil
	}
	return &eventpb.ReportResponse{RunId: &runID}, nil
}

// shadows reports whether the migration state and the candidate let a start event reach the
// candidate. A type no node lists quarantines the candidate, because the state is incomplete.
func (r *referenceService) shadows(ctx context.Context, eventType string) bool {
	if types, ok := r.migration.Get(); ok {
		listed, found := types[eventType]
		if !found {
			r.quarantiner.Quarantine(ctx, reason(model.QuarantineUnlisted, model.Ingress, eventType,
				"event type "+eventType+" is in no node of the migration state"))
			return false
		}
		if !listed.migrated {
			return false
		}
	}
	return !quarantined(r.directions...)
}

// record reports a produced event to egress in the candidate's terms. A result the scripts
// cannot map fails the exchange, which quarantines a candidate event that claims it.
func (r *referenceService) record(ctx context.Context, id engine.RequestID, event *eventpb.Event, result Option[*eventpb.Result]) error {
	produced, declared := r.rules.producedBy(event.GetType())
	route := r.rules.route(event.GetType())
	payload := event.GetPayload()
	decision, err := r.recorder.ProcessRequest(ctx, Some(id), route, None[string](), func(ctx context.Context) (schema.Value, error) {
		if mapping, maps := produced.Payload.Get(); declared && maps {
			return r.mapper.mapValue(ctx, mapping, payload)
		}
		return r.mapper.decode(payload, r.rules.comparedType(route))
	})
	if err != nil {
		return errors.Wrap(err, "record reference event")
	}
	forward, ok := decision.(engine.DecisionForward)
	if !ok {
		return errors.Errorf("egress answered a reference event with %T", decision)
	}
	recorded, err := r.candidateResult(ctx, produced.Result, result)
	if err != nil {
		r.recorder.Unable(ctx, forward.Exchange, errors.Wrap(err, "map the candidate's result"))
		return nil
	}
	r.recorder.ProcessResponse(ctx, forward.Exchange, engine.Response{Raw: recorded, Decode: undecoded})
	return nil
}

// candidateResult is the result a candidate event that claims the reference's receives. A failed
// send's failure passes unchanged, and so does any result the scripts do not map.
func (r *referenceService) candidateResult(ctx context.Context, declared Option[script.Mapping], result Option[*eventpb.Result]) (recordedResult, error) {
	reported, ok := result.Get()
	mapping, maps := declared.Get()
	if !ok || !maps {
		return newRecordedResult(result), nil
	}
	if _, failed := reported.GetResult().(*eventpb.Result_Failure); failed {
		return newRecordedResult(result), nil
	}
	mapped, err := r.mapper.mapJSON(ctx, mapping, reported.GetMessage())
	if err != nil {
		return newRecordedResult(None[*eventpb.Result]()), err
	}
	return newRecordedResult(Some(&eventpb.Result{Result: &eventpb.Result_Message{Message: mapped}})), nil
}

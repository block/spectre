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
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/schema"
	"github.com/block/spectre/spectrepb/eventpb"
	"github.com/block/spectre/spectrepb/eventpb/eventpbconnect"
)

// candidateService streams the candidate its runs and pairs the events they produce. Every
// event the candidate reports is something its work produced: it never starts work itself.
type candidateService struct {
	mapper *mapper
	rules  rules
	// receivers admits the candidate's Receive streams, which ingress runs start.
	receivers *proxy.Candidates
	// starts quarantines a candidate that reports an event that starts work.
	starts   engine.Quarantiner
	egress   protocols.Side
	triggers *triggers
}

func newCandidateService(mapper *mapper, rules rules, receivers *proxy.Candidates, starts engine.Quarantiner, egress protocols.Side, triggers *triggers) *candidateService {
	return &candidateService{mapper: mapper, rules: rules, receivers: receivers, starts: starts, egress: egress, triggers: triggers}
}

var _ eventpbconnect.EventServiceHandler = (*candidateService)(nil)

// Report pairs a produced event with the reference's, and returns the reference's result.
func (c *candidateService) Report(ctx context.Context, request *eventpb.ReportRequest) (*eventpb.ReportResponse, error) {
	event := request.GetEvent()
	if event == nil || event.GetType() == "" {
		return nil, invalidArgument("report needs an event with a type")
	}
	ctx, finish, admitted := admit(ctx, c.egress.Candidates)
	if !admitted {
		return nil, connect.NewError(connect.CodeUnavailable, "candidate work is refused")
	}
	defer finish()
	logger.FromContext(ctx).DebugContext(ctx, "Candidate reported an event",
		"event", logger.EventCorrelation, "type", event.GetType(), "attributes", event.GetAttributes())
	if c.rules.candidateStartsWork(event.GetType()) {
		c.starts.Quarantine(ctx, reason(model.QuarantineUnmatched, model.Ingress, event.GetType(),
			"the candidate reported an event that starts work, which only the reference does"))
		return nil, connect.NewError(connect.CodeFailedPrecondition, "only the reference starts work")
	}
	id := None[engine.RequestID]()
	if request.RunId != nil {
		parsed, err := typeid.Parse[engine.RequestID](request.GetRunId())
		if err != nil {
			c.egress.Quarantiner.Quarantine(ctx, reason(model.QuarantinePairing, model.Egress, event.GetType(), "run ID is malformed"))
			return nil, invalidArgument("run ID is malformed")
		}
		id = Some(parsed)
	}
	route := model.Route(event.GetType()) //nolint:typeboundary // protobuf getters return plain strings
	payload := event.GetPayload()
	decision, err := c.egress.Candidate.ProcessRequest(ctx, id, route, None[string](), func(context.Context) (schema.Value, error) {
		return c.mapper.decode(payload, c.rules.comparedType(route))
	})
	if err != nil {
		// Egress has quarantined the candidate, unless the mirror stopped its work.
		return nil, connect.NewError(connect.CodeAborted, err.Error())
	}
	return answer(decision)
}

// answer is the candidate's response to the egress decision on its event.
func answer(decision engine.Decision) (*eventpb.ReportResponse, error) {
	switch decision := decision.(type) {
	case engine.DecisionIntercept:
		defer decision.Raw.Release()
		recorded, ok := decision.Raw.(recordedResult)
		if !ok {
			return nil, connect.NewError(connect.CodeInternal, "egress replayed a result the event server did not record")
		}
		return recorded.response(), nil
	case engine.DecisionForward, engine.DecisionAnswer:
		return nil, connect.NewError(connect.CodeInternal, errors.Errorf("egress answered a candidate event with %T", decision).Error())
	}
	return nil, connect.NewError(connect.CodeInternal, "egress made no decision")
}

// Receive streams the candidate each run the reference starts. A run whose send fails is lost,
// and the reference's events in it expire unclaimed.
func (c *candidateService) Receive(ctx context.Context, _ *eventpb.ReceiveRequest, stream eventpbconnect.EventServiceReceiveServerStream) error {
	ctx, finish, admitted := admit(ctx, c.receivers)
	if !admitted {
		return connect.NewError(connect.CodeUnavailable, "candidate work is refused")
	}
	defer finish()
	queue := c.triggers.open()
	defer c.triggers.close()
	// Headers tell the candidate that runs can now reach it.
	if err := stream.SendHeaders(); err != nil {
		return errors.Wrap(err, "send headers")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case trigger := <-queue:
			if err := stream.Send(trigger); err != nil {
				return errors.Wrap(err, "send run")
			}
		}
	}
}

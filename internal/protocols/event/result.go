package event

import (
	"context"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/spectrepb/eventpb"
)

// recordedResult is what the reference's send returned, already in the candidate's terms,
// which egress replays to the candidate event that claims it.
type recordedResult struct {
	result Option[*eventpb.Result]
}

func newRecordedResult(result Option[*eventpb.Result]) recordedResult {
	return recordedResult{result: result}
}

// Release frees nothing: a result is small and is not counted against a budget.
func (recordedResult) Release() {}

// response answers the candidate event that claimed the result.
func (r recordedResult) response() *eventpb.ReportResponse {
	response := &eventpb.ReportResponse{}
	if result, ok := r.result.Get(); ok {
		response.Result = result
	}
	return response
}

// undecoded is the content of a reference result. No replacement reads event results, so
// egress never decodes them.
func undecoded(context.Context) engine.Content {
	return engine.Content{Failure: Some(errors.New("event results are not decoded"))}
}

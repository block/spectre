package engine_test

import (
	"context"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

const (
	listRoute = model.Route("POST fake.example/list")
	getRoute  = model.Route("POST fake.example/get")
)

const replacementScript = `
import * as spectre from "spectre";
import type * as v1 from "test.v1";
spectre.egress.fake("POST fake.example/list").match<v1.Query>();
spectre.egress.fake("POST fake.example/get").replaces<v1.Query, v1.Response>("POST fake.example/list").using<v1.Query, v1.Response>(
  (request) => ({ value: request.value }),
  (request, reference) => ({ value: reference.value + "/" + request.value }),
);
spectre.field<v1.Query, "trace">(() => undefined);
`

func TestEgressAnswersReplacingRequestFromSourceReference(t *testing.T) {
	h := newScriptHarness(t, replacementScript, time.Second)
	raw := newRaw("list")
	exchange := h.forward(t, listRoute, query(t, queryType, "london", "reference"))
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, listed(t, raw))

	replaced, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", "candidate"))
	assert.NoError(t, err)
	// Replacing never claims the source reference, so it still pairs with a candidate request to the source.
	matched, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), listRoute, notIgnored, query(t, queryType, "london", "candidate"))
	assert.NoError(t, err)
	again, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))
	assert.NoError(t, err)

	answer := engine.Decision(engine.DecisionAnswer{Message: instance(t, responseType, map[string]any{"value": "listed/london"})})
	assert.Equal(t, answer, replaced)
	assert.Equal(t, answer, again)
	assert.Equal(t, engine.Decision(engine.DecisionIntercept{Raw: raw}), matched)
	assert.Equal(t, nil, h.quarantine.Reasons())
	assert.Equal(t, 2, len(h.records(t, "Candidate request was answered from a reference response")))
	assert.Equal(t, map[string]any{
		"level": "INFO", "msg": "Candidate request was answered from a reference response",
		"event": "correlation", "protocol": "fake", "route": string(getRoute), "source": string(listRoute),
	}, h.records(t, "Candidate request was answered from a reference response")[0])
	assert.Equal(t, 0, raw.Releases())
}

func TestEgressReplacingRequestWaitsForSourceReference(t *testing.T) {
	for name, hashedFirst := range map[string]bool{
		"Request":  false,
		"Response": true,
	} {
		t.Run(name, func(t *testing.T) {
			h := newScriptHarness(t, replacementScript, 200*time.Millisecond)
			var exchange engine.Exchange
			if hashedFirst {
				exchange = h.forward(t, listRoute, query(t, queryType, "london", ""))
			}
			decided := make(chan candidateDecision, 1)
			go func() {
				decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))
				decided <- candidateDecision{decision: decision, err: err}
			}()
			time.Sleep(20 * time.Millisecond)
			if !hashedFirst {
				exchange = h.forward(t, listRoute, query(t, queryType, "london", ""))
			}

			raw := newRaw("list")
			h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, listed(t, raw))

			assert.Equal(t, candidateDecision{decision: engine.DecisionAnswer{
				Message: instance(t, responseType, map[string]any{"value": "listed/london"}),
			}}, <-decided)
			// The replacing request matched the source reference, so it expires without quarantine.
			awaitRelease(t, raw)
			assert.Equal(t, nil, h.quarantine.Reasons())
		})
	}
}

func TestEgressReplacingRequestPairsWithReferenceRequestsToItsOwnEndpoint(t *testing.T) {
	h := newScriptHarness(t, replacementScript, time.Second)
	own := newRaw("get")
	exchange := h.forward(t, getRoute, query(t, queryType, "london", ""))
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, engine.Response{Raw: own})

	decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))

	assert.NoError(t, err)
	assert.Equal(t, engine.Decision(engine.DecisionIntercept{Raw: own}), decision)
	assert.Equal(t, nil, h.quarantine.Reasons())
	assert.Equal(t, 1, len(h.records(t, "Candidate request matched a reference request")))
}

func TestEgressReplacingRequestPrefersReferenceToItsOwnEndpoint(t *testing.T) {
	h := newScriptHarness(t, replacementScript, time.Second)
	source := h.forward(t, listRoute, query(t, queryType, "london", ""))
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, source, listed(t, newRaw("list")))
	exchange := h.forward(t, getRoute, query(t, queryType, "london", ""))
	decided := make(chan candidateDecision, 1)
	go func() {
		decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))
		decided <- candidateDecision{decision: decision, err: err}
	}()
	// The source has answered, but the request waits for the reference to its own endpoint.
	time.Sleep(20 * time.Millisecond)
	own := newRaw("get")
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, engine.Response{Raw: own})
	first := <-decided
	assert.NoError(t, first.err)
	// The source reference was not claimed, so it still answers the next request.
	second, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))
	assert.NoError(t, err)

	assert.Equal(t, engine.Decision(engine.DecisionIntercept{Raw: own}), first.decision)
	assert.Equal(t, 0, own.Releases())
	assert.Equal(t, engine.Decision(engine.DecisionAnswer{Message: instance(t, responseType, map[string]any{"value": "listed/london"})}), second)
	assert.Equal(t, nil, h.quarantine.Reasons())
}

func TestEgressReplacingRequestLeavesLaterReferencesToItsOwnEndpoint(t *testing.T) {
	h := newScriptHarness(t, replacementScript, time.Second)
	decided := make(chan candidateDecision, 1)
	go func() {
		decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))
		decided <- candidateDecision{decision: decision, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	source := h.forward(t, listRoute, query(t, queryType, "london", ""))
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, source, listed(t, newRaw("list")))
	replaced := <-decided
	own := newRaw("get")
	exchange := h.forward(t, getRoute, query(t, queryType, "london", ""))
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, engine.Response{Raw: own})

	replayed, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))

	assert.NoError(t, replaced.err)
	assert.Equal(t, engine.Decision(engine.DecisionAnswer{Message: instance(t, responseType, map[string]any{"value": "listed/london"})}), replaced.decision)
	assert.NoError(t, err)
	assert.Equal(t, engine.Decision(engine.DecisionIntercept{Raw: own}), replayed)
	assert.Equal(t, 0, own.Releases())
	assert.Equal(t, nil, h.quarantine.Reasons())
}

func TestEgressReleasesDecodedSourceResponseWhenForgotten(t *testing.T) {
	for name, respondLate := range map[string]bool{
		"Responded":      false,
		"RespondedLater": true,
	} {
		t.Run(name, func(t *testing.T) {
			h := newScriptHarness(t, replacementScript, 50*time.Millisecond)
			raw, hold := newRaw("list"), newRaw("hold")
			exchange := h.forward(t, listRoute, query(t, queryType, "london", ""))
			if respondLate {
				time.Sleep(100 * time.Millisecond)
			}
			response := listed(t, raw)
			decode := response.Decode
			response.Decode = func(ctx context.Context) engine.Content {
				content := decode(ctx)
				content.Hold = Some[engine.Raw](hold)
				return content
			}
			h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, response)
			if !respondLate {
				_, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))
				assert.NoError(t, err)
				assert.Equal(t, 0, hold.Releases())
			}

			awaitRelease(t, hold)
		})
	}
}

func TestEgressKeepsDecodedSourceResponseWhenReplayIsCancelled(t *testing.T) {
	h := newScriptHarness(t, replacementScript, 200*time.Millisecond)
	exchange := h.forward(t, listRoute, query(t, queryType, "london", ""))
	ctx, cancel := context.WithTimeout(h.ctx, 20*time.Millisecond)
	defer cancel()
	_, err := h.engine.EgressCandidate(fake).ProcessRequest(ctx, Some(h.requestID), listRoute, notIgnored, query(t, queryType, "london", ""))
	assert.Error(t, err)

	raw, hold := newRaw("list"), newRaw("hold")
	response := listed(t, raw)
	decode := response.Decode
	response.Decode = func(ctx context.Context) engine.Content {
		content := decode(ctx)
		content.Hold = Some[engine.Raw](hold)
		return content
	}
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, response)
	replaced, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))
	assert.NoError(t, err)

	assert.Equal(t, engine.Decision(engine.DecisionAnswer{Message: instance(t, responseType, map[string]any{"value": "listed/london"})}), replaced)
	assert.Equal(t, 1, raw.Releases())
	assert.Equal(t, 0, hold.Releases(), "the decoded response is still offered to replacements")
	awaitRelease(t, hold)
}

func TestEgressReleasesDecodedSourceResponseThatWasNeverOffered(t *testing.T) {
	h := newScriptHarness(t, replacementScript, time.Second)
	truncated := func(context.Context) (schema.Value, error) { return nil, errors.New("truncated") }
	exchange := h.forward(t, listRoute, truncated)

	hold := newRaw("hold")
	response := listed(t, newRaw("list"))
	decode := response.Decode
	response.Decode = func(ctx context.Context) engine.Content {
		content := decode(ctx)
		content.Hold = Some[engine.Raw](hold)
		return content
	}
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, response)

	assert.Equal(t, 1, hold.Releases())
}

func TestEgressQuarantinesUnanswerableReplacingRequests(t *testing.T) {
	for name, test := range map[string]struct {
		window   time.Duration
		source   Option[string]
		response Option[func(raw engine.Raw) engine.Response]
		fails    bool
		// expires lets the window pass before the candidate request arrives.
		expires  bool
		expected string
		kind     model.QuarantineKind
	}{
		"DifferentSourceRequest": {
			window: 50 * time.Millisecond, source: Some("paris"),
			expected: "no matching reference or source reference request within 50ms", kind: model.QuarantineUnmatched,
		},
		"SourceExpired": {
			window: 20 * time.Millisecond, source: Some("london"), expires: true,
			expected: "no matching reference or source reference request within 20ms", kind: model.QuarantineUnmatched,
		},
		"SourceFailed": {
			window: time.Second, source: Some("london"), fails: true,
			expected: "source reference response failed: connection reset", kind: model.QuarantineUnable,
		},
		"SourceUndecodable": {
			window: time.Second, source: Some("london"),
			response: Some(func(raw engine.Raw) engine.Response {
				return engine.Response{Raw: raw, Decode: func(context.Context) engine.Content {
					return engine.Content{Status: Some("200"), Failure: Some(errors.New("truncated"))}
				}}
			}),
			expected: "decode source reference response: truncated", kind: model.QuarantineUnable,
		},
		"SourceWithoutMessage": {
			window: time.Second, source: Some("london"),
			response: Some(func(raw engine.Raw) engine.Response {
				return engine.Response{Raw: raw, Decode: func(context.Context) engine.Content {
					return engine.Content{Status: Some("204")}
				}}
			}),
			expected: "source reference response has no message", kind: model.QuarantineUnable,
		},
		"SourceResponseMistyped": {
			window: time.Second, source: Some("london"),
			response: Some(func(raw engine.Raw) engine.Response {
				return engine.Response{Raw: raw, Decode: func(context.Context) engine.Content {
					return engine.Content{Status: Some("200"), Message: Some(instance(t, unknown, map[string]any{}))}
				}}
			}),
			expected: "translate response: source reference response is unknown, not test.v1.Response", kind: model.QuarantineUnable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newScriptHarness(t, replacementScript, test.window)
			if source, ok := test.source.Get(); ok {
				exchange := h.forward(t, listRoute, query(t, queryType, source, ""))
				raw := newRaw("list")
				response, responds := test.response.Get()
				switch {
				case test.fails:
					h.engine.EgressReference(fake).Unable(h.ctx, exchange, errors.New("connection reset"))
				case responds:
					h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, response(raw))
				default:
					h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, listed(t, newRaw("list")))
				}
			}
			if test.expires {
				time.Sleep(2 * test.window)
			}

			decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))

			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.expected)
			assert.Equal(t, nil, decision)
			// A source reference nothing matched also quarantines, on its own route.
			recorded := slices.DeleteFunc(h.quarantine.Recorded(), func(reason model.QuarantineReason) bool {
				traffic, _ := reason.Traffic.Get()
				return traffic.Route != getRoute
			})
			assert.Equal(t, 1, len(recorded))
			assert.Equal(t, test.kind, recorded[0].Kind)
			assert.Equal(t, err.Error(), recorded[0].Detail)
		})
	}
}

func TestEgressRefusesReplacingRequestWhoseSourceWasCancelled(t *testing.T) {
	h := newScriptHarness(t, replacementScript, time.Second)
	exchange := h.forward(t, listRoute, query(t, queryType, "london", ""))
	cancelled, cancel := context.WithCancel(h.ctx)
	cancel()
	h.engine.EgressReference(fake).Unable(cancelled, exchange, errors.New("connection reset"))

	decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))

	assert.EqualError(t, err, "source reference request was cancelled: connection reset")
	assert.Equal(t, nil, decision)
	assert.Equal(t, nil, h.quarantine.Reasons())
}

func TestEgressQuarantinesReplacingRequestThatGivesUp(t *testing.T) {
	h := newScriptHarness(t, replacementScript, time.Second)
	ctx, cancel := context.WithTimeout(h.ctx, 20*time.Millisecond)
	defer cancel()

	decision, err := h.engine.EgressCandidate(fake).ProcessRequest(ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))

	assert.Error(t, err)
	assert.True(t, regexp.MustCompile(`^no matching reference or source reference request before the candidate gave up after \d+m?s: context deadline exceeded$`).MatchString(err.Error()), "error: %s", err)
	assert.Equal(t, nil, decision)
	assert.Equal(t, []model.QuarantineReason{{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: fake, Route: getRoute}),
		Detail:  err.Error(),
	}}, h.quarantine.Recorded())
}

func TestEgressReplacingRequestStoppedByProxyIsNotQuarantined(t *testing.T) {
	h := newScriptHarness(t, replacementScript, time.Second)
	ctx, cancel := context.WithCancelCause(h.ctx)
	cancel(engine.ErrCandidateStopped)

	_, err := h.engine.EgressCandidate(fake).ProcessRequest(ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))

	assert.True(t, errors.Is(err, context.Canceled))
	assert.Equal(t, nil, h.quarantine.Reasons())
}

func TestEgressQuarantinesReplacingRequestThatGivesUpOnSourceResponse(t *testing.T) {
	h := newScriptHarness(t, replacementScript, time.Second)
	h.forward(t, listRoute, query(t, queryType, "london", "reference"))
	ctx, cancel := context.WithTimeout(h.ctx, 20*time.Millisecond)
	defer cancel()

	_, err := h.engine.EgressCandidate(fake).ProcessRequest(ctx, Some(h.requestID), getRoute, notIgnored, query(t, queryType, "london", ""))

	assert.Equal(t, "wait for source reference response: context deadline exceeded", err.Error())
	assert.Equal(t, []model.QuarantineReason{{
		Kind:    model.QuarantineUnable,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: fake, Route: getRoute}),
		Detail:  err.Error(),
	}}, h.quarantine.Recorded())
}

func TestEngineReportsSourceResponseTypes(t *testing.T) {
	h := newScriptHarness(t, replacementScript, time.Second)

	assert.Equal(t, Some[schema.TypeRef]("test.v1.Response"), h.engine.EgressSourceResponse(fake, listRoute))
	assert.Equal(t, None[schema.TypeRef](), h.engine.EgressSourceResponse(fake, getRoute))
	assert.Equal(t, None[schema.TypeRef](), h.engine.EgressSourceResponse(other, listRoute))
}

// listed is a source reference response that decodes to "listed".
func listed(t *testing.T, raw engine.Raw) engine.Response {
	t.Helper()
	message := instance(t, responseType, map[string]any{"value": "listed"})
	return engine.Response{Raw: raw, Decode: func(context.Context) engine.Content {
		return engine.Content{Status: Some("200"), Message: Some(message)}
	}}
}

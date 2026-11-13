package protocol_test

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/schema"
)

const (
	listRoute = model.Route("POST fake.example/list")
	getRoute  = model.Route("POST fake.example/get")
)

const deriveScript = `
import * as spectre from "spectre";
import type * as v1 from "test.v1";
spectre.egress.http("POST fake.example/list").match<v1.Query>();
spectre.egress.http("POST fake.example/get").derive<v1.Query, v1.Response>().from<v1.Query, v1.Response>(
  "POST fake.example/list",
  (request) => ({ value: request.value }),
  (request, reference) => ({ value: reference.value + "/" + request.value }),
);
spectre.field<v1.Query, "trace">(() => undefined);
`

var derivedType = Some[schema.TypeRef]("test.v1.Response")

func TestEgressAnswersDerivedRequestFromSourceReference(t *testing.T) {
	h := newScriptHarness(t, deriveScript, time.Second)
	raw := newRaw("list")
	exchange := h.forwardHTTP(t, listRoute, query(queryType, "london", "reference"))
	h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, listed(raw))

	derived, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", "candidate"))
	assert.NoError(t, err)
	// Deriving never claims the source reference, so it still pairs with a candidate request to the source.
	matched, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), listRoute, notIgnored, query(queryType, "london", "candidate"))
	assert.NoError(t, err)
	again, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))
	assert.NoError(t, err)

	answer := protocol.Decision(protocol.DecisionAnswer{Message: protocol.Message{Type: derivedType, Value: map[string]any{"value": "listed/london"}}})
	assert.Equal(t, answer, derived)
	assert.Equal(t, answer, again)
	assert.Equal(t, protocol.Decision(protocol.DecisionIntercept{Raw: raw}), matched)
	assert.Equal(t, nil, h.quarantine.Reasons())
	assert.Equal(t, 2, len(h.records(t, "Candidate request was answered from a reference response")))
	assert.Equal(t, map[string]any{
		"level": "INFO", "msg": "Candidate request was answered from a reference response",
		"event": "correlation", "protocol": "http", "route": string(getRoute), "source": string(listRoute),
	}, h.records(t, "Candidate request was answered from a reference response")[0])
	assert.Equal(t, 0, raw.Releases())
}

func TestEgressDerivedRequestWaitsForSourceReference(t *testing.T) {
	for name, hashedFirst := range map[string]bool{
		"Request":  false,
		"Response": true,
	} {
		t.Run(name, func(t *testing.T) {
			h := newScriptHarness(t, deriveScript, time.Second)
			var exchange protocol.Exchange
			if hashedFirst {
				exchange = h.forwardHTTP(t, listRoute, query(queryType, "london", ""))
			}
			decided := make(chan candidateDecision, 1)
			go func() {
				decision, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))
				decided <- candidateDecision{decision: decision, err: err}
			}()
			time.Sleep(20 * time.Millisecond)
			if !hashedFirst {
				exchange = h.forwardHTTP(t, listRoute, query(queryType, "london", ""))
			}

			h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, listed(newRaw("list")))

			assert.Equal(t, candidateDecision{decision: protocol.DecisionAnswer{
				Message: protocol.Message{Type: derivedType, Value: map[string]any{"value": "listed/london"}},
			}}, <-decided)
			assert.Equal(t, nil, h.quarantine.Reasons())
		})
	}
}

func TestEgressDerivedRequestPairsWithReferenceRequestsToItsOwnEndpoint(t *testing.T) {
	h := newScriptHarness(t, deriveScript, time.Second)
	own := newRaw("get")
	exchange := h.forwardHTTP(t, getRoute, query(queryType, "london", ""))
	h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, protocol.Response{Raw: own})

	decision, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))

	assert.NoError(t, err)
	assert.Equal(t, protocol.Decision(protocol.DecisionIntercept{Raw: own}), decision)
	assert.Equal(t, nil, h.quarantine.Reasons())
	assert.Equal(t, 1, len(h.records(t, "Candidate request matched a reference request")))
}

func TestEgressDerivedRequestPrefersReferenceToItsOwnEndpoint(t *testing.T) {
	h := newScriptHarness(t, deriveScript, time.Second)
	source := h.forwardHTTP(t, listRoute, query(queryType, "london", ""))
	h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, source, listed(newRaw("list")))
	exchange := h.forwardHTTP(t, getRoute, query(queryType, "london", ""))
	decided := make(chan candidateDecision, 1)
	go func() {
		decision, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))
		decided <- candidateDecision{decision: decision, err: err}
	}()
	// The source has answered, but the request waits for the reference to its own endpoint.
	time.Sleep(20 * time.Millisecond)
	own := newRaw("get")
	h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, protocol.Response{Raw: own})
	first := <-decided
	assert.NoError(t, first.err)
	// The source reference was not claimed, so it still answers the next request.
	second, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))
	assert.NoError(t, err)

	assert.Equal(t, protocol.Decision(protocol.DecisionIntercept{Raw: own}), first.decision)
	assert.Equal(t, 0, own.Releases())
	assert.Equal(t, protocol.Decision(protocol.DecisionAnswer{Message: protocol.Message{Type: derivedType, Value: map[string]any{"value": "listed/london"}}}), second)
	assert.Equal(t, nil, h.quarantine.Reasons())
}

func TestEgressDerivedRequestLeavesLaterReferencesToItsOwnEndpoint(t *testing.T) {
	h := newScriptHarness(t, deriveScript, time.Second)
	decided := make(chan candidateDecision, 1)
	go func() {
		decision, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))
		decided <- candidateDecision{decision: decision, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	source := h.forwardHTTP(t, listRoute, query(queryType, "london", ""))
	h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, source, listed(newRaw("list")))
	derived := <-decided
	own := newRaw("get")
	exchange := h.forwardHTTP(t, getRoute, query(queryType, "london", ""))
	h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, protocol.Response{Raw: own})

	replayed, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))

	assert.NoError(t, derived.err)
	assert.Equal(t, protocol.Decision(protocol.DecisionAnswer{Message: protocol.Message{Type: derivedType, Value: map[string]any{"value": "listed/london"}}}), derived.decision)
	assert.NoError(t, err)
	assert.Equal(t, protocol.Decision(protocol.DecisionIntercept{Raw: own}), replayed)
	assert.Equal(t, 0, own.Releases())
	assert.Equal(t, nil, h.quarantine.Reasons())
}

func TestEgressReleasesDecodedSourceResponseWhenForgotten(t *testing.T) {
	for name, respondLate := range map[string]bool{
		"Responded":      false,
		"RespondedLater": true,
	} {
		t.Run(name, func(t *testing.T) {
			h := newScriptHarness(t, deriveScript, 50*time.Millisecond)
			raw, hold := newRaw("list"), newRaw("hold")
			exchange := h.forwardHTTP(t, listRoute, query(queryType, "london", ""))
			if respondLate {
				time.Sleep(100 * time.Millisecond)
			}
			response := listed(raw)
			decode := response.Decode
			response.Decode = func(ctx context.Context) protocol.Content {
				content := decode(ctx)
				content.Hold = Some[protocol.Raw](hold)
				return content
			}
			h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, response)
			if !respondLate {
				_, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))
				assert.NoError(t, err)
				assert.Equal(t, 0, hold.Releases())
			}

			awaitRelease(t, hold)
		})
	}
}

func TestEgressKeepsDecodedSourceResponseWhenReplayIsCancelled(t *testing.T) {
	h := newScriptHarness(t, deriveScript, 200*time.Millisecond)
	exchange := h.forwardHTTP(t, listRoute, query(queryType, "london", ""))
	ctx, cancel := context.WithTimeout(h.ctx, 20*time.Millisecond)
	defer cancel()
	_, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(ctx, None[protocol.RequestID](), listRoute, notIgnored, query(queryType, "london", ""))
	assert.Error(t, err)

	raw, hold := newRaw("list"), newRaw("hold")
	response := listed(raw)
	decode := response.Decode
	response.Decode = func(ctx context.Context) protocol.Content {
		content := decode(ctx)
		content.Hold = Some[protocol.Raw](hold)
		return content
	}
	h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, response)
	derived, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))
	assert.NoError(t, err)

	assert.Equal(t, protocol.Decision(protocol.DecisionAnswer{Message: protocol.Message{Type: derivedType, Value: map[string]any{"value": "listed/london"}}}), derived)
	assert.Equal(t, 1, raw.Releases())
	assert.Equal(t, 0, hold.Releases(), "the decoded response is still offered to derivations")
	awaitRelease(t, hold)
}

func TestEgressReleasesDecodedSourceResponseThatWasNeverOffered(t *testing.T) {
	h := newScriptHarness(t, deriveScript, time.Second)
	truncated := func(context.Context) (protocol.Message, error) { return protocol.Message{}, errors.New("truncated") }
	exchange := h.forwardHTTP(t, listRoute, truncated)

	hold := newRaw("hold")
	response := listed(newRaw("list"))
	decode := response.Decode
	response.Decode = func(ctx context.Context) protocol.Content {
		content := decode(ctx)
		content.Hold = Some[protocol.Raw](hold)
		return content
	}
	h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, response)

	assert.Equal(t, 1, hold.Releases())
}

func TestEgressQuarantinesUnderivableRequests(t *testing.T) {
	for name, test := range map[string]struct {
		window   time.Duration
		source   Option[string]
		response Option[func(raw protocol.Raw) protocol.Response]
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
			response: Some(func(raw protocol.Raw) protocol.Response {
				return protocol.Response{Raw: raw, Decode: func(context.Context) protocol.Content {
					return protocol.Content{Status: Some("200"), Failure: Some(errors.New("truncated"))}
				}}
			}),
			expected: "decode source reference response: truncated", kind: model.QuarantineUnable,
		},
		"SourceWithoutMessage": {
			window: time.Second, source: Some("london"),
			response: Some(func(raw protocol.Raw) protocol.Response {
				return protocol.Response{Raw: raw, Decode: func(context.Context) protocol.Content {
					return protocol.Content{Status: Some("204")}
				}}
			}),
			expected: "source reference response has no message", kind: model.QuarantineUnable,
		},
		"DerivationThrows": {
			window: time.Second, source: Some("london"),
			response: Some(func(raw protocol.Raw) protocol.Response {
				return protocol.Response{Raw: raw, Decode: func(context.Context) protocol.Content {
					return protocol.Content{Status: Some("200"), Message: Some(protocol.Message{Type: derivedType, Value: nil})}
				}}
			}),
			expected: "derive response: ", kind: model.QuarantineUnable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newScriptHarness(t, deriveScript, test.window)
			if source, ok := test.source.Get(); ok {
				exchange := h.forwardHTTP(t, listRoute, query(queryType, source, ""))
				raw := newRaw("list")
				response, responds := test.response.Get()
				switch {
				case test.fails:
					h.engine.EgressReference(model.ProtocolHTTP).Unable(h.ctx, exchange, errors.New("connection reset"))
				case responds:
					h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, response(raw))
				default:
					h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, listed(newRaw("list")))
				}
			}
			if test.expires {
				time.Sleep(2 * test.window)
			}

			decision, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))

			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.expected)
			assert.Equal(t, nil, decision)
			recorded := h.quarantine.Recorded()
			assert.Equal(t, 1, len(recorded))
			assert.Equal(t, test.kind, recorded[0].Kind)
			assert.Equal(t, err.Error(), recorded[0].Detail)
		})
	}
}

func TestEgressQuarantinesDerivedRequestThatGivesUp(t *testing.T) {
	h := newScriptHarness(t, deriveScript, time.Second)
	ctx, cancel := context.WithTimeout(h.ctx, 20*time.Millisecond)
	defer cancel()

	decision, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))

	assert.Error(t, err)
	assert.True(t, regexp.MustCompile(`^no matching reference or source reference request before the candidate gave up after \d+m?s: context deadline exceeded$`).MatchString(err.Error()), "error: %s", err)
	assert.Equal(t, nil, decision)
	assert.Equal(t, []model.QuarantineReason{{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: getRoute}),
		Detail:  err.Error(),
	}}, h.quarantine.Recorded())
}

func TestEgressDerivedRequestStoppedByProxyIsNotQuarantined(t *testing.T) {
	h := newScriptHarness(t, deriveScript, time.Second)
	ctx, cancel := context.WithCancelCause(h.ctx)
	cancel(protocol.ErrCandidateStopped)

	_, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))

	assert.True(t, errors.Is(err, context.Canceled))
	assert.Equal(t, nil, h.quarantine.Reasons())
}

func TestEgressDerivedRequestGivesUpOnSourceResponse(t *testing.T) {
	h := newScriptHarness(t, deriveScript, time.Second)
	h.forwardHTTP(t, listRoute, query(queryType, "london", "reference"))
	ctx, cancel := context.WithTimeout(h.ctx, 20*time.Millisecond)
	defer cancel()

	_, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(ctx, None[protocol.RequestID](), getRoute, notIgnored, query(queryType, "london", ""))

	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.Equal(t, nil, h.quarantine.Reasons())
}

func TestEngineReportsSourceResponseTypes(t *testing.T) {
	h := newScriptHarness(t, deriveScript, time.Second)

	assert.Equal(t, derivedType, h.engine.EgressSourceResponse(model.ProtocolHTTP, listRoute))
	assert.Equal(t, None[schema.TypeRef](), h.engine.EgressSourceResponse(model.ProtocolHTTP, getRoute))
	assert.Equal(t, None[schema.TypeRef](), h.engine.EgressSourceResponse(fake, listRoute))
}

// forwardHTTP sends an HTTP reference request and waits until it is hashed.
func (h *harness) forwardHTTP(t *testing.T, route model.Route, request func(ctx context.Context) (protocol.Message, error)) protocol.Exchange {
	t.Helper()
	decision, err := h.engine.EgressReference(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), route, notIgnored, request)
	assert.NoError(t, err)
	assert.NoError(t, h.engine.Shutdown(h.ctx))
	return decision.(protocol.DecisionForward).Exchange
}

// listed is a source reference response that decodes to "listed".
func listed(raw protocol.Raw) protocol.Response {
	return protocol.Response{Raw: raw, Decode: func(context.Context) protocol.Content {
		return protocol.Content{Status: Some("200"), Message: Some(protocol.Message{Type: derivedType, Value: map[string]any{"value": "listed"}})}
	}}
}

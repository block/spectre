package engine_test

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

var queryType = &schema.ObjectType{Name: "test.v1.Query", Fields: textFields}

func TestEgressCandidatePairsWithEquivalentReference(t *testing.T) {
	t.Parallel()
	const window = 200 * time.Millisecond
	for name, test := range map[string]struct {
		referenceRoute   model.Route
		referenceRequest func(ctx context.Context) (schema.Value, error)
		candidateRoute   model.Route
		candidateRequest func(ctx context.Context) (schema.Value, error)
		matched          bool
		// differences are how the candidate differs from the reference it nearly matches, if any.
		differences []model.Difference
	}{
		"Normalised": {
			referenceRoute: "query", referenceRequest: query(t, queryType, "london", "first"),
			candidateRoute: "query", candidateRequest: query(t, queryType, "london", "second"),
			matched: true,
		},
		"UntypedEqual": {
			referenceRoute: "query", referenceRequest: query(t, unknown, "london", "first"),
			candidateRoute: "query", candidateRequest: query(t, unknown, "london", "first"),
			matched: true,
		},
		"UntypedSkipsNormalisers": {
			referenceRoute: "query", referenceRequest: query(t, unknown, "london", "first"),
			candidateRoute: "query", candidateRequest: query(t, unknown, "london", "second"),
			differences: []model.Difference{{Path: "$[*]", Change: model.ChangeChanged, Count: 1}},
		},
		"DifferentValue": {
			referenceRoute: "query", referenceRequest: query(t, queryType, "london", "first"),
			candidateRoute: "query", candidateRequest: query(t, queryType, "paris", "second"),
			differences: []model.Difference{{Path: "$.value", Change: model.ChangeChanged, Count: 1}},
		},
		"DifferentRoute": {
			referenceRoute: "query", referenceRequest: query(t, queryType, "london", ""),
			candidateRoute: "other-query", candidateRequest: query(t, queryType, "london", ""),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, window)
			raw := newRaw("reference")
			h.reference(t, test.referenceRoute, test.referenceRequest, raw)

			decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), test.candidateRoute, notIgnored, test.candidateRequest)

			if test.matched {
				assert.NoError(t, err)
				assert.Equal(t, engine.Decision(engine.DecisionIntercept{Raw: raw}), decision)
				assert.Equal(t, nil, h.quarantine.Reasons())
				assert.Equal(t, []map[string]any{{
					"level": "INFO", "msg": "Candidate request matched a reference request",
					"event": "correlation", "protocol": "fake", "route": string(test.candidateRoute),
				}}, h.records(t, "Candidate request matched a reference request"))
				assert.Equal(t, 0, raw.Releases())
				return
			}
			if len(test.differences) > 0 {
				assert.EqualError(t, err, "request differs from the only unclaimed reference request on the route: "+
					strings.Join(model.DifferenceLines(test.differences, false), ", "))
				assert.Equal(t, nil, decision)
				awaitRelease(t, raw)
				// The reference was claimed to be diffed, so it never expires as left out.
				time.Sleep(2 * window)
				assert.Equal(t, []model.QuarantineReason{{
					Kind:        model.QuarantineDivergent,
					Traffic:     Some(model.Traffic{Direction: model.Egress, Protocol: fake, Route: test.candidateRoute}),
					Differences: test.differences,
				}}, h.quarantine.Recorded())
				return
			}
			assert.EqualError(t, err, "no matching reference request within 200ms")
			assert.Equal(t, nil, decision)
			awaitRelease(t, raw)
			// Each side made a request the other did not, and the windows may close in either order.
			recorded := h.quarantine.Recorded()
			slices.SortFunc(recorded, func(a, b model.QuarantineReason) int { return strings.Compare(a.Detail, b.Detail) })
			assert.Equal(t, []model.QuarantineReason{
				{
					Kind:    model.QuarantineUnmatched,
					Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: fake, Route: test.referenceRoute}),
					Detail:  "no matching candidate request within 200ms",
				},
				{
					Kind:    model.QuarantineUnmatched,
					Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: fake, Route: test.candidateRoute}),
					Detail:  "no matching reference request within 200ms",
				},
			}, recorded)
		})
	}
}

func TestEgressPairsDuplicatesOldestFirst(t *testing.T) {
	h := newHarness(t, time.Second)
	first, second := newRaw("first"), newRaw("second")
	h.reference(t, "query", query(t, queryType, "london", ""), first)
	h.reference(t, "query", query(t, queryType, "london", ""), second)

	firstDecision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), "query", notIgnored, query(t, queryType, "london", ""))
	assert.NoError(t, err)
	secondDecision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), "query", notIgnored, query(t, queryType, "london", ""))
	assert.NoError(t, err)

	assert.Equal(t, []engine.Decision{engine.DecisionIntercept{Raw: first}, engine.DecisionIntercept{Raw: second}},
		[]engine.Decision{firstDecision, secondDecision})
}

func TestEgressCandidateClaimsEarliestArrivedReference(t *testing.T) {
	h := newHarness(t, time.Second)
	unblock := make(chan struct{})
	first, second := newRaw("first"), newRaw("second")
	// The first reference arrives first but finishes hashing last.
	firstExchange := h.send(t, "query", blockedUntil(unblock, query(t, queryType, "london", "")))
	secondExchange := h.send(t, "query", query(t, queryType, "london", ""))
	// Give the second reference time to be hashed before the first.
	time.Sleep(50 * time.Millisecond)
	close(unblock)
	assert.NoError(t, h.engine.Shutdown(h.ctx))
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, firstExchange, engine.Response{Raw: first})
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, secondExchange, engine.Response{Raw: second})

	firstDecision := <-h.candidate(t, query(t, queryType, "london", ""))
	secondDecision := <-h.candidate(t, query(t, queryType, "london", ""))

	assert.Equal(t, []candidateDecision{
		{decision: engine.DecisionIntercept{Raw: first}},
		{decision: engine.DecisionIntercept{Raw: second}},
	}, []candidateDecision{firstDecision, secondDecision})
}

func TestEgressCandidateDoesNotWaitForEarlierReferenceStillHashing(t *testing.T) {
	h := newHarness(t, time.Second)
	unblock := make(chan struct{})
	defer close(unblock)
	second := newRaw("second")
	h.send(t, "query", blockedUntil(unblock, query(t, queryType, "london", "")))
	secondExchange := h.send(t, "query", query(t, queryType, "london", ""))
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, secondExchange, engine.Response{Raw: second})

	decision := <-h.candidate(t, query(t, queryType, "london", ""))

	assert.Equal(t, candidateDecision{decision: engine.DecisionIntercept{Raw: second}}, decision)
}

func TestEgressEarliestArrivedCandidateGetsFirstReference(t *testing.T) {
	h := newHarness(t, time.Second)
	unblock, started := make(chan struct{}), make(chan struct{})
	first, second := newRaw("first"), newRaw("second")
	payload := query(t, queryType, "london", "")
	// The first candidate arrives first but finishes hashing last.
	firstDecided := h.candidate(t, func(ctx context.Context) (schema.Value, error) {
		close(started)
		<-unblock
		return payload(ctx)
	})
	<-started
	secondDecided := h.candidate(t, payload)
	time.Sleep(50 * time.Millisecond)
	close(unblock)
	// Give the first candidate time to start waiting before any reference arrives.
	time.Sleep(50 * time.Millisecond)
	firstExchange := h.forward(t, "query", payload)
	secondExchange := h.forward(t, "query", payload)
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, firstExchange, engine.Response{Raw: first})
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, secondExchange, engine.Response{Raw: second})

	assert.Equal(t, []candidateDecision{
		{decision: engine.DecisionIntercept{Raw: first}},
		{decision: engine.DecisionIntercept{Raw: second}},
	}, []candidateDecision{<-firstDecided, <-secondDecided})
}

func TestEgressCandidateWaitsForReference(t *testing.T) {
	for name, hashedFirst := range map[string]bool{
		"Request":  false,
		"Response": true,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, time.Second)
			raw := newRaw("reference")
			var exchange engine.Exchange
			if hashedFirst {
				exchange = h.forward(t, "query", query(t, queryType, "london", ""))
			}
			decided := make(chan engine.Decision, 1)
			go func() {
				decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), "query", notIgnored, query(t, queryType, "london", ""))
				assert.NoError(t, err)
				decided <- decision
			}()
			time.Sleep(20 * time.Millisecond)
			if !hashedFirst {
				exchange = h.forward(t, "query", query(t, queryType, "london", ""))
			}

			h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, engine.Response{Raw: raw})

			assert.Equal(t, engine.Decision(engine.DecisionIntercept{Raw: raw}), <-decided)
			assert.Equal(t, nil, h.quarantine.Reasons())
		})
	}
}

func TestEgressCandidateGivesUpWhenCancelled(t *testing.T) {
	const (
		waitingForReference = `^no matching reference request before the candidate gave up after \d+m?s: context deadline exceeded$`
		waitingForResponse  = `^wait for reference response: context deadline exceeded$`
	)
	for name, test := range map[string]struct {
		referenced bool
		// stopped cancels the candidate as the proxy does when it stops candidate work.
		stopped bool
		// expected matches the error the candidate gets.
		expected string
		// kind is empty when the candidate is not quarantined.
		kind model.QuarantineKind
	}{
		"WaitingForReference":        {expected: waitingForReference, kind: model.QuarantineUnmatched},
		"WaitingForResponse":         {referenced: true, expected: waitingForResponse, kind: model.QuarantineUnable},
		"StoppedWaitingForReference": {stopped: true, expected: waitingForReference},
		"StoppedWaitingForResponse":  {referenced: true, stopped: true, expected: waitingForResponse},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, time.Second)
			var exchange engine.Exchange
			if test.referenced {
				exchange = h.forward(t, "query", query(t, queryType, "london", ""))
			}
			var cause error
			if test.stopped {
				cause = engine.ErrCandidateStopped
			}
			ctx, cancel := context.WithTimeoutCause(h.ctx, 20*time.Millisecond, cause)
			defer cancel()

			decision, err := h.engine.EgressCandidate(fake).ProcessRequest(ctx, Some(h.requestID), "query", notIgnored, query(t, queryType, "london", ""))

			assert.Error(t, err)
			assert.True(t, regexp.MustCompile(test.expected).MatchString(err.Error()), "error: %s", err)
			assert.True(t, errors.Is(err, context.DeadlineExceeded))
			assert.Equal(t, nil, decision)
			if test.kind != "" {
				assert.Equal(t, []model.QuarantineReason{{
					Kind:    test.kind,
					Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: fake, Route: "query"}),
					Detail:  err.Error(),
				}}, h.quarantine.Recorded())
			} else {
				assert.Equal(t, nil, h.quarantine.Reasons())
			}
			if test.referenced {
				// The abandoned reference is released as soon as it completes.
				raw := newRaw("reference")
				h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, engine.Response{Raw: raw})
				assert.Equal(t, 1, raw.Releases())
			}
		})
	}
}

func TestEgressQuarantinesUnreplayableRequests(t *testing.T) {
	truncated := func(context.Context) (schema.Value, error) { return nil, errors.New("truncated") }
	for name, test := range map[string]struct {
		referenceRequest func(ctx context.Context) (schema.Value, error)
		referenceFails   bool
		// referenceCancelled reports the failure after the reference gave up on the request.
		referenceCancelled bool
		candidateRequest   Option[func(ctx context.Context) (schema.Value, error)]
		// candidateError is empty when only the reference is quarantined.
		candidateError string
		quarantined    []string
		// released is how often the reference response is released.
		released int
	}{
		"UnidentifiableReference": {
			referenceRequest: truncated,
			quarantined:      []string{"identify reference request: truncated"},
			released:         1,
		},
		"UnidentifiableCandidate": {
			referenceRequest: query(t, queryType, "london", ""),
			candidateRequest: Some(truncated),
			candidateError:   "identify candidate request: truncated",
			quarantined:      []string{"identify candidate request: truncated"},
		},
		"ReferenceUnable": {
			referenceRequest: query(t, queryType, "london", ""),
			referenceFails:   true,
			candidateRequest: Some(query(t, queryType, "london", "")),
			candidateError:   "reference response cannot be replayed: connection reset",
			quarantined:      []string{"reference response cannot be replayed: connection reset"},
		},
		"ReferenceCancelled": {
			referenceRequest:   query(t, queryType, "london", ""),
			referenceFails:     true,
			referenceCancelled: true,
			candidateRequest:   Some(query(t, queryType, "london", "")),
			candidateError:     "reference request was cancelled: connection reset",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, time.Second)
			raw := newRaw("reference")
			exchange := h.forward(t, "query", test.referenceRequest)
			if test.referenceFails {
				ctx, cancel := context.WithCancel(h.ctx)
				if test.referenceCancelled {
					cancel()
				}
				h.engine.EgressReference(fake).Unable(ctx, exchange, errors.New("connection reset"))
				cancel()
			} else {
				h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, engine.Response{Raw: raw})
			}

			if candidateRequest, ok := test.candidateRequest.Get(); ok {
				decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), "query", notIgnored, candidateRequest)
				assert.EqualError(t, err, test.candidateError)
				assert.Equal(t, nil, decision)
			}

			assert.Equal(t, test.quarantined, h.quarantine.Reasons())
			assert.Equal(t, test.released, raw.Releases())
		})
	}
}

func TestEgressQuarantinesUnclaimedReferenceAfterWindow(t *testing.T) {
	for name, test := range map[string]struct {
		script   string
		protocol model.Protocol
		route    model.Route
		forward  func(h *harness, t *testing.T, route model.Route, request func(ctx context.Context) (schema.Value, error)) engine.Exchange
	}{
		"Reference":       {script: fakeScript, protocol: fake, route: "query", forward: (*harness).forward},
		"SourceReference": {script: replacementScript, protocol: fake, route: listRoute, forward: (*harness).forward},
	} {
		t.Run(name, func(t *testing.T) {
			h := newScriptHarness(t, test.script, 20*time.Millisecond)
			raw := newRaw("reference")
			exchange := test.forward(h, t, test.route, query(t, queryType, "london", ""))
			h.engine.EgressReference(test.protocol).ProcessResponse(h.ctx, exchange, listed(t, raw))

			awaitRelease(t, raw)

			assert.Equal(t, []model.QuarantineReason{{
				Kind:    model.QuarantineUnmatched,
				Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: test.protocol, Route: test.route}),
				Detail:  "no matching candidate request within 20ms",
			}}, h.quarantine.Recorded())
			_, err := h.engine.EgressCandidate(test.protocol).ProcessRequest(h.ctx, Some(h.requestID), test.route, notIgnored, query(t, queryType, "london", ""))
			assert.EqualError(t, err, "no matching reference request within 20ms")
		})
	}
}

func TestEgressNearMatchesReferenceThatArrivesWhileCandidateWaits(t *testing.T) {
	h := newHarness(t, 100*time.Millisecond)
	decided := h.candidate(t, query(t, queryType, "paris", ""))
	time.Sleep(20 * time.Millisecond)
	raw := newRaw("reference")
	h.reference(t, "query", query(t, queryType, "london", ""), raw)

	outcome := <-decided

	assert.EqualError(t, outcome.err, "request differs from the only unclaimed reference request on the route: ~ $.value")
	awaitRelease(t, raw)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, []string{"request differences: ~ $.value"}, h.quarantine.Reasons())
}

func TestEgressDoesNotGuessBetweenSeveralNearMatches(t *testing.T) {
	const window = 50 * time.Millisecond
	h := newHarness(t, window)
	london, berlin := newRaw("london"), newRaw("berlin")
	h.reference(t, "query", query(t, queryType, "london", ""), london)
	h.reference(t, "query", query(t, queryType, "berlin", ""), berlin)

	_, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), "query", notIgnored, query(t, queryType, "paris", ""))

	assert.EqualError(t, err, "no matching reference request within 50ms: 2 unclaimed reference requests on the route, so none is compared")
	// Neither reference was claimed, so the candidate left both out.
	awaitRelease(t, london)
	awaitRelease(t, berlin)
	// A held reference may expire as the candidate gives up, so reasons arrive in either order.
	recorded := h.quarantine.Recorded()
	slices.SortFunc(recorded, func(a, b model.QuarantineReason) int { return strings.Compare(a.Detail, b.Detail) })
	traffic := Some(model.Traffic{Direction: model.Egress, Protocol: fake, Route: "query"})
	assert.Equal(t, []model.QuarantineReason{
		{Kind: model.QuarantineUnmatched, Traffic: traffic, Detail: "no matching candidate request within 50ms"},
		{Kind: model.QuarantineUnmatched, Traffic: traffic, Detail: "no matching candidate request within 50ms"},
		{Kind: model.QuarantineUnmatched, Traffic: traffic, Detail: err.Error()},
	}, recorded)
}

func TestEgressNeverNearMatchesAcrossMirroredRequests(t *testing.T) {
	h := newHarness(t, 50*time.Millisecond)
	raw := newRaw("reference")
	h.reference(t, "query", query(t, queryType, "london", ""), raw)

	outcome := <-h.candidateIn(t, Some(newRequestID()), query(t, queryType, "paris", ""))

	assert.EqualError(t, outcome.err, "no matching reference request within 50ms")
}

const optionalScript = `
import * as spectre from "spectre";
import type * as v1 from "test.v1";
spectre.egress.fake("POST fake.example/list").optional().match<v1.Query>();
spectre.egress.fake("POST fake.example/get").optional().replaces<v1.Query, v1.Response>("POST fake.example/list").using<v1.Query, v1.Response>(
  (request) => ({ value: request.value }),
  (request, reference) => ({ value: reference.value + "/" + request.value }),
);
spectre.field<v1.Query, "trace">(() => undefined);
`

func TestEgressReleasesUnclaimedOptionalReferenceWithoutQuarantine(t *testing.T) {
	for name, route := range map[string]model.Route{"Source": listRoute, "Replacing": getRoute} {
		t.Run(name, func(t *testing.T) {
			h := newScriptHarness(t, optionalScript, 20*time.Millisecond)
			raw := newRaw("reference")
			exchange := h.forward(t, route, query(t, queryType, "london", ""))
			h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, listed(t, raw))

			awaitRelease(t, raw)

			assert.Equal(t, nil, h.quarantine.Reasons())
			assert.Equal(t, []map[string]any{{
				"level": "INFO", "msg": "Candidate left out an optional reference request",
				"event": "correlation", "protocol": "fake", "route": string(route),
			}}, h.records(t, "Candidate left out an optional reference request"))
		})
	}
}

func TestEgressQuarantinesUnmatchedCandidateToOptionalEndpoint(t *testing.T) {
	h := newScriptHarness(t, optionalScript, 20*time.Millisecond)

	_, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, Some(h.requestID), listRoute, notIgnored, query(t, queryType, "london", ""))

	assert.EqualError(t, err, "no matching reference request within 20ms")
	assert.Equal(t, []model.QuarantineReason{{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: fake, Route: listRoute}),
		Detail:  "no matching reference request within 20ms",
	}}, h.quarantine.Recorded())
}

func TestEgressReleasesUnclaimedReferenceAfterCloseWithoutQuarantine(t *testing.T) {
	h := newHarness(t, 20*time.Millisecond)
	raw := newRaw("reference")
	h.reference(t, "query", query(t, queryType, "london", ""), raw)

	h.engine.Close()

	awaitRelease(t, raw)
	assert.Equal(t, nil, h.quarantine.Reasons())
}

func TestEgressPairsRequestsOnlyWithinTheirMirroredRequest(t *testing.T) {
	h := newHarness(t, 100*time.Millisecond)
	raw := newRaw("reference")
	other := h.candidateIn(t, Some(newRequestID()), query(t, queryType, "london", ""))

	h.reference(t, "query", query(t, queryType, "london", ""), raw)
	same := <-h.candidate(t, query(t, queryType, "london", ""))

	assert.Equal(t, candidateDecision{decision: engine.DecisionIntercept{Raw: raw}}, same)
	decided := <-other
	assert.EqualError(t, decided.err, "no matching reference request within 100ms")
	assert.Equal(t, []model.QuarantineReason{{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: fake, Route: "query"}),
		Detail:  "no matching reference request within 100ms",
	}}, h.quarantine.Recorded())
}

func TestEgressQuarantinesCandidateRequestWithoutRequestID(t *testing.T) {
	h := newHarness(t, time.Second)

	decided := <-h.candidateIn(t, None[engine.RequestID](), query(t, queryType, "london", ""))

	assert.EqualError(t, decided.err, "candidate request carries no SPECTRE request ID")
	assert.Equal(t, []model.QuarantineReason{{
		Kind:    model.QuarantinePairing,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: fake, Route: "query"}),
		Detail:  "candidate request carries no SPECTRE request ID",
	}}, h.quarantine.Recorded())
}

func TestEgressForwardsReferenceRequestWithoutRequestIDUnpaired(t *testing.T) {
	h := newHarness(t, 20*time.Millisecond)
	raw := newRaw("reference")

	decision, err := h.engine.EgressReference(fake).ProcessRequest(h.ctx, None[engine.RequestID](), "query", notIgnored, unreadPayload(t))
	assert.NoError(t, err)
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, decision.(engine.DecisionForward).Exchange, engine.Response{Raw: raw})

	assert.Equal(t, 1, raw.Releases())
	decided := <-h.candidate(t, query(t, queryType, "london", ""))
	assert.EqualError(t, decided.err, "no matching reference request within 20ms")
	assert.Equal(t, []model.QuarantineReason{{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: fake, Route: "query"}),
		Detail:  "no matching reference request within 20ms",
	}}, h.quarantine.Recorded())
}

func TestEgressCandidateReleasesReportedResponse(t *testing.T) {
	h := newHarness(t, time.Second)
	raw := newRaw("candidate")

	h.engine.EgressCandidate(fake).ProcessResponse(h.ctx, engine.Exchange{}, engine.Response{Raw: raw})

	assert.Equal(t, 1, raw.Releases())
	assert.Equal(t, 1, len(h.records(t, "Egress candidate reported a response")))
}

func TestShutdownWaitsForReferenceHashing(t *testing.T) {
	h := newHarness(t, time.Second)
	unblock := make(chan struct{})
	payload := query(t, queryType, "london", "")
	_, err := h.engine.EgressReference(fake).ProcessRequest(h.ctx, Some(h.requestID), "query", notIgnored,
		func(ctx context.Context) (schema.Value, error) {
			<-unblock
			return payload(ctx)
		})
	assert.NoError(t, err)
	ctx, cancel := context.WithTimeout(h.ctx, 20*time.Millisecond)
	defer cancel()

	assert.EqualError(t, h.engine.Shutdown(ctx), "wait for reference hashing: context deadline exceeded")

	close(unblock)
	assert.NoError(t, h.engine.Shutdown(h.ctx))
}

// send forwards a reference request without waiting for it to be hashed.
func (h *harness) send(t *testing.T, route model.Route, request func(ctx context.Context) (schema.Value, error)) engine.Exchange {
	t.Helper()
	decision, err := h.engine.EgressReference(fake).ProcessRequest(h.ctx, Some(h.requestID), route, notIgnored, request)
	assert.NoError(t, err)
	forward := decision.(engine.DecisionForward)
	assert.Equal(t, None[engine.RequestID](), forward.RequestID)
	return forward.Exchange
}

// forward sends a reference request and waits until it is hashed.
func (h *harness) forward(t *testing.T, route model.Route, request func(ctx context.Context) (schema.Value, error)) engine.Exchange {
	t.Helper()
	exchange := h.send(t, route, request)
	assert.NoError(t, h.engine.Shutdown(h.ctx))
	return exchange
}

// blockedUntil delays request until unblock closes, so a test controls when it is hashed.
func blockedUntil(unblock <-chan struct{}, request func(ctx context.Context) (schema.Value, error)) func(ctx context.Context) (schema.Value, error) {
	return func(ctx context.Context) (schema.Value, error) {
		<-unblock
		return request(ctx)
	}
}

// candidateDecision is the outcome of one egress candidate request.
type candidateDecision struct {
	decision engine.Decision
	err      error
}

// candidate sends a candidate request in the background.
func (h *harness) candidate(t *testing.T, request func(ctx context.Context) (schema.Value, error)) <-chan candidateDecision {
	t.Helper()
	return h.candidateIn(t, Some(h.requestID), request)
}

// candidateIn sends a candidate request that belongs to requestID in the background.
func (h *harness) candidateIn(t *testing.T, requestID Option[engine.RequestID], request func(ctx context.Context) (schema.Value, error)) <-chan candidateDecision {
	t.Helper()
	decided := make(chan candidateDecision, 1)
	go func() {
		decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, requestID, "query", notIgnored, request)
		decided <- candidateDecision{decision: decision, err: err}
	}()
	return decided
}

// reference forwards a hashed reference request and records its response.
func (h *harness) reference(t *testing.T, route model.Route, request func(ctx context.Context) (schema.Value, error), raw *countingRaw) {
	t.Helper()
	exchange := h.forward(t, route, request)
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, engine.Response{Raw: raw})
}

// query returns a fresh payload on every call, because normalisation mutates it.
func query(t *testing.T, declared schema.Type, value, trace string) func(ctx context.Context) (schema.Value, error) {
	t.Helper()
	request := instance(t, declared, map[string]any{"value": value, "trace": trace})
	return func(context.Context) (schema.Value, error) {
		return request, nil
	}
}

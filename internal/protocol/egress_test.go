package protocol_test

import (
	"context"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/quarantine"
	"github.com/block/spectre/internal/schema"
)

var queryType = Some[schema.TypeRef]("test.v1.Query")

func TestEgressCandidatePairsWithEquivalentReference(t *testing.T) {
	t.Parallel()
	const window = 200 * time.Millisecond
	for name, test := range map[string]struct {
		referenceRoute   boundary.Route
		referenceRequest func(ctx context.Context) (protocol.Message, error)
		candidateRoute   boundary.Route
		candidateRequest func(ctx context.Context) (protocol.Message, error)
		matched          bool
	}{
		"Normalised": {
			referenceRoute: "query", referenceRequest: query(queryType, "london", "first"),
			candidateRoute: "query", candidateRequest: query(queryType, "london", "second"),
			matched: true,
		},
		"UntypedEqual": {
			referenceRoute: "query", referenceRequest: query(None[schema.TypeRef](), "london", "first"),
			candidateRoute: "query", candidateRequest: query(None[schema.TypeRef](), "london", "first"),
			matched: true,
		},
		"UntypedSkipsNormalisers": {
			referenceRoute: "query", referenceRequest: query(None[schema.TypeRef](), "london", "first"),
			candidateRoute: "query", candidateRequest: query(None[schema.TypeRef](), "london", "second"),
		},
		"DifferentValue": {
			referenceRoute: "query", referenceRequest: query(queryType, "london", ""),
			candidateRoute: "query", candidateRequest: query(queryType, "paris", ""),
		},
		"DifferentRoute": {
			referenceRoute: "query", referenceRequest: query(queryType, "london", ""),
			candidateRoute: "other-query", candidateRequest: query(queryType, "london", ""),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, window)
			raw := newRaw("reference")
			h.reference(t, test.referenceRoute, test.referenceRequest, raw)

			decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), test.candidateRoute, notIgnored, test.candidateRequest)

			if test.matched {
				assert.NoError(t, err)
				assert.Equal(t, protocol.Decision(protocol.DecisionIntercept{Raw: raw}), decision)
				assert.Equal(t, nil, h.quarantine.Reasons())
				assert.Equal(t, []map[string]any{{
					"level": "INFO", "msg": "Candidate request matched a reference request",
					"event": "correlation", "protocol": "fake", "route": string(test.candidateRoute),
				}}, h.records(t, "Candidate request matched a reference request"))
				assert.Equal(t, 0, raw.Releases())
				return
			}
			assert.EqualError(t, err, "no matching reference request within 200ms")
			assert.Equal(t, nil, decision)
			assert.Equal(t, []quarantine.Reason{{
				Kind:    quarantine.KindUnmatched,
				Traffic: Some(quarantine.Traffic{Direction: boundary.Egress, Protocol: fake, Route: test.candidateRoute}),
				Detail:  "no matching reference request within 200ms",
			}}, h.quarantine.Recorded())
			awaitReleases(t, raw, 1)
		})
	}
}

func TestEgressPairsDuplicatesOldestFirst(t *testing.T) {
	h := newHarness(t, time.Second)
	first, second := newRaw("first"), newRaw("second")
	h.reference(t, "query", query(queryType, "london", ""), first)
	h.reference(t, "query", query(queryType, "london", ""), second)

	firstDecision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), "query", notIgnored, query(queryType, "london", ""))
	assert.NoError(t, err)
	secondDecision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), "query", notIgnored, query(queryType, "london", ""))
	assert.NoError(t, err)

	assert.Equal(t, []protocol.Decision{protocol.DecisionIntercept{Raw: first}, protocol.DecisionIntercept{Raw: second}},
		[]protocol.Decision{firstDecision, secondDecision})
}

func TestEgressCandidateClaimsEarliestArrivedReference(t *testing.T) {
	h := newHarness(t, time.Second)
	unblock := make(chan struct{})
	first, second := newRaw("first"), newRaw("second")
	// The first reference arrives first but finishes hashing last.
	firstExchange := h.send(t, "query", blockedUntil(unblock, query(queryType, "london", "")))
	secondExchange := h.send(t, "query", query(queryType, "london", ""))
	// Give the second reference time to be hashed before the first.
	time.Sleep(50 * time.Millisecond)
	close(unblock)
	assert.NoError(t, h.engine.Shutdown(h.ctx))
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, firstExchange, protocol.Response{Raw: first})
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, secondExchange, protocol.Response{Raw: second})

	firstDecision := <-h.candidate(t, query(queryType, "london", ""))
	secondDecision := <-h.candidate(t, query(queryType, "london", ""))

	assert.Equal(t, []candidateDecision{
		{decision: protocol.DecisionIntercept{Raw: first}},
		{decision: protocol.DecisionIntercept{Raw: second}},
	}, []candidateDecision{firstDecision, secondDecision})
}

func TestEgressCandidateDoesNotWaitForEarlierReferenceStillHashing(t *testing.T) {
	h := newHarness(t, time.Second)
	unblock := make(chan struct{})
	defer close(unblock)
	second := newRaw("second")
	h.send(t, "query", blockedUntil(unblock, query(queryType, "london", "")))
	secondExchange := h.send(t, "query", query(queryType, "london", ""))
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, secondExchange, protocol.Response{Raw: second})

	decision := <-h.candidate(t, query(queryType, "london", ""))

	assert.Equal(t, candidateDecision{decision: protocol.DecisionIntercept{Raw: second}}, decision)
}

func TestEgressEarliestArrivedCandidateGetsFirstReference(t *testing.T) {
	h := newHarness(t, time.Second)
	unblock, started := make(chan struct{}), make(chan struct{})
	first, second := newRaw("first"), newRaw("second")
	payload := query(queryType, "london", "")
	// The first candidate arrives first but finishes hashing last.
	firstDecided := h.candidate(t, func(ctx context.Context) (protocol.Message, error) {
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
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, firstExchange, protocol.Response{Raw: first})
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, secondExchange, protocol.Response{Raw: second})

	assert.Equal(t, []candidateDecision{
		{decision: protocol.DecisionIntercept{Raw: first}},
		{decision: protocol.DecisionIntercept{Raw: second}},
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
			var exchange protocol.Exchange
			if hashedFirst {
				exchange = h.forward(t, "query", query(queryType, "london", ""))
			}
			decided := make(chan protocol.Decision, 1)
			go func() {
				decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), "query", notIgnored, query(queryType, "london", ""))
				assert.NoError(t, err)
				decided <- decision
			}()
			time.Sleep(20 * time.Millisecond)
			if !hashedFirst {
				exchange = h.forward(t, "query", query(queryType, "london", ""))
			}

			h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, protocol.Response{Raw: raw})

			assert.Equal(t, protocol.Decision(protocol.DecisionIntercept{Raw: raw}), <-decided)
			assert.Equal(t, nil, h.quarantine.Reasons())
		})
	}
}

func TestEgressCandidateGivesUpWhenCancelled(t *testing.T) {
	for name, test := range map[string]struct {
		referenced bool
		expected   string
	}{
		"WaitingForReference": {expected: "wait for matching reference request: context deadline exceeded"},
		"WaitingForResponse":  {referenced: true, expected: "wait for reference response: context deadline exceeded"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, time.Second)
			var exchange protocol.Exchange
			if test.referenced {
				exchange = h.forward(t, "query", query(queryType, "london", ""))
			}
			ctx, cancel := context.WithTimeout(h.ctx, 20*time.Millisecond)
			defer cancel()

			decision, err := h.engine.EgressCandidate(fake).ProcessRequest(ctx, None[protocol.RequestID](), "query", notIgnored, query(queryType, "london", ""))

			assert.EqualError(t, err, test.expected)
			assert.True(t, errors.Is(err, context.DeadlineExceeded))
			assert.Equal(t, nil, decision)
			assert.Equal(t, nil, h.quarantine.Reasons())
			if test.referenced {
				// The abandoned reference is released as soon as it completes.
				raw := newRaw("reference")
				h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, protocol.Response{Raw: raw})
				assert.Equal(t, 1, raw.Releases())
			}
		})
	}
}

func TestEgressQuarantinesUnreplayableRequests(t *testing.T) {
	truncated := func(context.Context) (protocol.Message, error) { return protocol.Message{}, errors.New("truncated") }
	for name, test := range map[string]struct {
		referenceRequest func(ctx context.Context) (protocol.Message, error)
		referenceFails   bool
		candidateRequest Option[func(ctx context.Context) (protocol.Message, error)]
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
			referenceRequest: query(queryType, "london", ""),
			candidateRequest: Some(truncated),
			candidateError:   "identify candidate request: truncated",
			quarantined:      []string{"identify candidate request: truncated"},
		},
		"ReferenceUnable": {
			referenceRequest: query(queryType, "london", ""),
			referenceFails:   true,
			candidateRequest: Some(query(queryType, "london", "")),
			candidateError:   "reference response cannot be replayed: connection reset",
			quarantined:      []string{"reference response cannot be replayed: connection reset"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, time.Second)
			raw := newRaw("reference")
			exchange := h.forward(t, "query", test.referenceRequest)
			if test.referenceFails {
				h.engine.EgressReference(fake).Unable(h.ctx, exchange, errors.New("connection reset"))
			} else {
				h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, protocol.Response{Raw: raw})
			}

			if candidateRequest, ok := test.candidateRequest.Get(); ok {
				decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), "query", notIgnored, candidateRequest)
				assert.EqualError(t, err, test.candidateError)
				assert.Equal(t, nil, decision)
			}

			assert.Equal(t, test.quarantined, h.quarantine.Reasons())
			assert.Equal(t, test.released, raw.Releases())
		})
	}
}

func TestEgressReleasesUnclaimedReferenceAfterWindow(t *testing.T) {
	h := newHarness(t, 20*time.Millisecond)
	raw := newRaw("reference")
	h.reference(t, "query", query(queryType, "london", ""), raw)

	awaitReleases(t, raw, 1)

	_, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), "query", notIgnored, query(queryType, "london", ""))
	assert.EqualError(t, err, "no matching reference request within 20ms")
}

func TestEgressCandidateReleasesReportedResponse(t *testing.T) {
	h := newHarness(t, time.Second)
	raw := newRaw("candidate")

	h.engine.EgressCandidate(fake).ProcessResponse(h.ctx, protocol.Exchange{}, protocol.Response{Raw: raw})

	assert.Equal(t, 1, raw.Releases())
	assert.Equal(t, 1, len(h.records(t, "Egress candidate reported a response")))
}

func TestShutdownWaitsForReferenceHashing(t *testing.T) {
	h := newHarness(t, time.Second)
	unblock := make(chan struct{})
	payload := query(queryType, "london", "")
	_, err := h.engine.EgressReference(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), "query", notIgnored,
		func(ctx context.Context) (protocol.Message, error) {
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
func (h *harness) send(t *testing.T, route boundary.Route, request func(ctx context.Context) (protocol.Message, error)) protocol.Exchange {
	t.Helper()
	decision, err := h.engine.EgressReference(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), route, notIgnored, request)
	assert.NoError(t, err)
	forward := decision.(protocol.DecisionForward)
	assert.Equal(t, None[protocol.RequestID](), forward.RequestID)
	return forward.Exchange
}

// forward sends a reference request and waits until it is hashed.
func (h *harness) forward(t *testing.T, route boundary.Route, request func(ctx context.Context) (protocol.Message, error)) protocol.Exchange {
	t.Helper()
	exchange := h.send(t, route, request)
	assert.NoError(t, h.engine.Shutdown(h.ctx))
	return exchange
}

// blockedUntil delays request until unblock closes, so a test controls when it is hashed.
func blockedUntil(unblock <-chan struct{}, request func(ctx context.Context) (protocol.Message, error)) func(ctx context.Context) (protocol.Message, error) {
	return func(ctx context.Context) (protocol.Message, error) {
		<-unblock
		return request(ctx)
	}
}

// candidateDecision is the outcome of one egress candidate request.
type candidateDecision struct {
	decision protocol.Decision
	err      error
}

// candidate sends a candidate request in the background.
func (h *harness) candidate(t *testing.T, request func(ctx context.Context) (protocol.Message, error)) <-chan candidateDecision {
	t.Helper()
	decided := make(chan candidateDecision, 1)
	go func() {
		decision, err := h.engine.EgressCandidate(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), "query", notIgnored, request)
		decided <- candidateDecision{decision: decision, err: err}
	}()
	return decided
}

// reference forwards a hashed reference request and records its response.
func (h *harness) reference(t *testing.T, route boundary.Route, request func(ctx context.Context) (protocol.Message, error), raw *countingRaw) {
	t.Helper()
	exchange := h.forward(t, route, request)
	h.engine.EgressReference(fake).ProcessResponse(h.ctx, exchange, protocol.Response{Raw: raw})
}

// query returns a fresh payload on every call, because normalisation mutates it.
func query(typeRef Option[schema.TypeRef], value, trace string) func(ctx context.Context) (protocol.Message, error) {
	return func(context.Context) (protocol.Message, error) {
		return protocol.Message{Type: typeRef, Value: map[string]any{"value": value, "trace": trace}}, nil
	}
}

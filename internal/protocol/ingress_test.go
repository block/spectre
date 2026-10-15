package protocol_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/schema"
)

var (
	responseType = Some[schema.TypeRef]("test.v1.Response")
	notIgnored   = None[string]()
	ignoredFor   = Some("ignored for test")
)

func TestIngressComparesResponses(t *testing.T) {
	for name, test := range map[string]struct {
		route       protocol.Route
		ignored     Option[string]
		reference   protocol.Response
		candidate   protocol.Response
		quarantined []string
		logged      map[string]any
	}{
		"Equivalent": {
			reference: protocol.Response{Decode: decoded("OK", message(responseType, "london", "first"))},
			candidate: protocol.Response{Decode: decoded("OK", message(responseType, "london", "second"))},
			logged:    map[string]any{"outcome": "equivalent"},
		},
		"NoMessages": {
			reference: protocol.Response{Decode: statusOnly("OK")},
			candidate: protocol.Response{Decode: statusOnly("OK")},
			logged:    map[string]any{"outcome": "equivalent"},
		},
		"DivergentMessages": {
			reference:   protocol.Response{Decode: decoded("OK", message(responseType, "london", ""))},
			candidate:   protocol.Response{Decode: decoded("OK", message(responseType, "paris", ""))},
			quarantined: []string{"response differences: $.value"},
			logged:      map[string]any{"outcome": "divergent", "differences": []any{"$.value"}},
		},
		"UntypedMessagesSkipNormalisers": {
			reference:   protocol.Response{Decode: decoded("OK", message(None[schema.TypeRef](), "london", "first"))},
			candidate:   protocol.Response{Decode: decoded("OK", message(None[schema.TypeRef](), "london", "second"))},
			quarantined: []string{"response differences: $.trace"},
			logged:      map[string]any{"outcome": "divergent", "differences": []any{"$.trace"}},
		},
		"Status": {
			reference:   protocol.Response{Decode: decoded("OK", message(responseType, "london", ""))},
			candidate:   protocol.Response{Decode: decoded("NOT_FOUND", message(responseType, "london", ""))},
			quarantined: []string{"response differences: $status"},
			logged:      map[string]any{"outcome": "divergent", "differences": []any{"$status"}},
		},
		"MissingCandidateMessage": {
			reference:   protocol.Response{Decode: decoded("OK", message(responseType, "london", ""))},
			candidate:   protocol.Response{Decode: statusOnly("OK")},
			quarantined: []string{"response differences: $"},
			logged:      map[string]any{"outcome": "divergent", "differences": []any{"$"}},
		},
		"DifferentMessageTypes": {
			reference:   protocol.Response{Decode: decoded("OK", message(responseType, "london", ""))},
			candidate:   protocol.Response{Decode: decoded("OK", message(None[schema.TypeRef](), "london", ""))},
			quarantined: []string{"response comparison failed: reference and candidate messages have different types"},
			logged:      map[string]any{"outcome": "unable", "reason": "reference and candidate messages have different types"},
		},
		"StatusDespiteReferenceDecodeFailure": {
			reference:   protocol.Response{Decode: undecodable(Some("OK"), "bad body")},
			candidate:   protocol.Response{Decode: statusOnly("NOT_FOUND")},
			quarantined: []string{"response differences: $status"},
			logged:      map[string]any{"outcome": "divergent", "differences": []any{"$status"}},
		},
		"StatusDespiteCandidateDecodeFailure": {
			reference:   protocol.Response{Decode: statusOnly("OK")},
			candidate:   protocol.Response{Decode: undecodable(Some("NOT_FOUND"), "bad body")},
			quarantined: []string{"response differences: $status"},
			logged:      map[string]any{"outcome": "divergent", "differences": []any{"$status"}},
		},
		"ReferenceDecodeFailure": {
			reference:   protocol.Response{Decode: undecodable(Some("OK"), "bad body")},
			candidate:   protocol.Response{Decode: statusOnly("OK")},
			quarantined: []string{"response comparison failed: reference: bad body"},
			logged:      map[string]any{"outcome": "unable", "reason": "reference: bad body"},
		},
		"CandidateDecodeFailure": {
			reference:   protocol.Response{Decode: statusOnly("OK")},
			candidate:   protocol.Response{Decode: undecodable(Some("OK"), "bad body")},
			quarantined: []string{"response comparison failed: candidate: bad body"},
			logged:      map[string]any{"outcome": "unable", "reason": "candidate: bad body"},
		},
		"ReferenceStatusUnknown": {
			reference:   protocol.Response{Decode: undecodable(None[string](), "no status")},
			candidate:   protocol.Response{Decode: statusOnly("OK")},
			quarantined: []string{"response comparison failed: reference: no status"},
			logged:      map[string]any{"outcome": "unable", "reason": "reference: no status"},
		},
		"CandidateStatusUnknown": {
			reference:   protocol.Response{Decode: statusOnly("OK")},
			candidate:   protocol.Response{Decode: undecodable(None[string](), "no status")},
			quarantined: []string{"response comparison failed: candidate: no status"},
			logged:      map[string]any{"outcome": "unable", "reason": "candidate: no status"},
		},
		"StatusUnknownWithoutFailure": {
			reference:   protocol.Response{Decode: statusOnly("OK")},
			candidate:   protocol.Response{Decode: func() protocol.Content { return protocol.Content{} }},
			quarantined: []string{"response comparison failed: response status is unknown"},
			logged:      map[string]any{"outcome": "unable", "reason": "response status is unknown"},
		},
		"Ignored": {
			route:     "ignored",
			ignored:   ignoredFor,
			reference:   protocol.Response{Decode: decoded("OK", message(responseType, "london", ""))},
			candidate:   protocol.Response{Decode: statusOnly("NOT_FOUND")},
			logged:      map[string]any{"outcome": "skipped", "reason": "ignored for test"},
		},
		"LatencyDelta": {
			reference: protocol.Response{Decode: statusOnly("OK"), Latency: 10 * time.Millisecond},
			candidate: protocol.Response{Decode: statusOnly("OK"), Latency: 15 * time.Millisecond},
			logged:    map[string]any{"outcome": "equivalent", "dt": float64(5 * time.Millisecond)},
		},
		"ReferenceLatencyUnmeasured": {
			reference: protocol.Response{Decode: statusOnly("OK")},
			candidate: protocol.Response{Decode: statusOnly("OK"), Latency: 15 * time.Millisecond},
			logged:    map[string]any{"outcome": "equivalent"},
		},
		"CandidateLatencyUnmeasured": {
			reference: protocol.Response{Decode: statusOnly("OK"), Latency: 10 * time.Millisecond},
			candidate: protocol.Response{Decode: statusOnly("OK")},
			logged:    map[string]any{"outcome": "equivalent"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, time.Second)
			route, ignored := protocol.Route("compared"), notIgnored
			if test.route != "" {
				route, ignored = test.route, test.ignored
			}
			referenceRaw, candidateRaw := newRaw("reference"), newRaw("candidate")
			test.reference.Raw, test.candidate.Raw = referenceRaw, candidateRaw
			reference, candidate := h.mirror(t, route, ignored)

			h.engine.IngressReference(fake).ProcessResponse(h.ctx, reference, test.reference)
			h.engine.IngressCandidate(fake).ProcessResponse(h.ctx, candidate, test.candidate)

			assert.Equal(t, test.quarantined, h.quarantine.Reasons())
			assert.Equal(t, []map[string]any{comparisonRecord(route, test.logged)}, h.records(t, "Response comparison completed"))
			assert.Equal(t, 1, referenceRaw.Releases())
			assert.Equal(t, 1, candidateRaw.Releases())
		})
	}
}

func TestIngressReportsUnableExchanges(t *testing.T) {
	for name, test := range map[string]struct {
		route          protocol.Route
		ignored        Option[string]
		referenceFails bool
		candidateFails bool
		// cancelled cancels the failing side's context.
		cancelled   bool
		quarantined []string
		compared    []map[string]any
		unfinished  []map[string]any
	}{
		"Reference": {
			referenceFails: true,
			quarantined:    []string{"response comparison failed: reference: connection reset"},
			compared:       []map[string]any{comparisonRecord("compared", map[string]any{"outcome": "unable", "reason": "reference: connection reset"})},
		},
		"ReferenceCancelled": {
			referenceFails: true,
			cancelled:      true,
			unfinished:     []map[string]any{unfinishedRecord("connection reset")},
		},
		"Candidate": {
			candidateFails: true,
			quarantined:    []string{"response comparison failed: candidate: connection reset"},
			compared:       []map[string]any{comparisonRecord("compared", map[string]any{"outcome": "unable", "reason": "candidate: connection reset"})},
		},
		"CandidateCancelled": {
			candidateFails: true,
			cancelled:      true,
		},
		"IgnoredCandidate": {
			route:          "ignored",
			ignored:        ignoredFor,
			candidateFails: true,
			compared:       []map[string]any{comparisonRecord("ignored", map[string]any{"outcome": "skipped", "reason": "ignored for test"})},
		},
		"IgnoredReference": {
			route:          "ignored",
			ignored:        ignoredFor,
			referenceFails: true,
			compared:       []map[string]any{comparisonRecord("ignored", map[string]any{"outcome": "skipped", "reason": "ignored for test"})},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, time.Second)
			route, ignored := protocol.Route("compared"), notIgnored
			if test.route != "" {
				route, ignored = test.route, test.ignored
			}
			failing := h.ctx
			if test.cancelled {
				cancelled, cancel := context.WithCancel(h.ctx)
				cancel()
				failing = cancelled
			}
			referenceRaw, candidateRaw := newRaw("reference"), newRaw("candidate")
			reference, candidate := h.mirror(t, route, ignored)
			reason := errors.New("connection reset")

			if test.referenceFails {
				h.engine.IngressReference(fake).Unable(failing, reference, reason)
			} else {
				h.engine.IngressReference(fake).ProcessResponse(h.ctx, reference, protocol.Response{Decode: statusOnly("OK"), Raw: referenceRaw})
			}
			if test.candidateFails {
				h.engine.IngressCandidate(fake).Unable(failing, candidate, reason)
			} else {
				h.engine.IngressCandidate(fake).ProcessResponse(h.ctx, candidate, protocol.Response{Decode: statusOnly("OK"), Raw: candidateRaw})
			}

			assert.Equal(t, test.quarantined, h.quarantine.Reasons())
			assert.Equal(t, append([]map[string]any{}, test.compared...), h.records(t, "Response comparison completed"))
			assert.Equal(t, append([]map[string]any{}, test.unfinished...), h.records(t, "Response comparison did not finish"))
			assert.Equal(t, boolToInt(!test.referenceFails), referenceRaw.Releases())
			assert.Equal(t, boolToInt(!test.candidateFails), candidateRaw.Releases())
		})
	}
}

func TestIngressCandidateMustJoinAReference(t *testing.T) {
	for name, test := range map[string]struct {
		candidate javascript.Protocol
		// joins is how many candidates join successfully first.
		joins            int
		referenceEnded   bool
		withoutRequestID bool
		// expected is formatted with the reference's RequestID.
		expected string
	}{
		"MissingRequestID": {
			candidate:        fake,
			withoutRequestID: true,
			expected:         "candidate request has no request ID",
		},
		"ReferenceEnded": {
			candidate:      fake,
			referenceEnded: true,
			expected:       "candidate request %s has no reference request",
		},
		"AlreadyJoined": {
			candidate: fake,
			joins:     1,
			expected:  "reference request %s already has a candidate",
		},
		"ProtocolMismatch": {
			candidate: other,
			expected:  "candidate request %s uses other, not fake",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, time.Second)
			decision, err := h.engine.IngressReference(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), "compared", notIgnored, unreadPayload(t))
			assert.NoError(t, err)
			forward := decision.(protocol.DecisionForward)
			requestID, ok := forward.RequestID.Get()
			assert.True(t, ok)
			for range test.joins {
				_, err := h.engine.IngressCandidate(fake).ProcessRequest(h.ctx, forward.RequestID, "compared", notIgnored, unreadPayload(t))
				assert.NoError(t, err)
			}
			if test.referenceEnded {
				h.engine.IngressReference(fake).ProcessResponse(h.ctx, forward.Exchange, protocol.Response{Decode: statusOnly("OK"), Raw: newRaw("reference")})
			}
			joining := forward.RequestID
			if test.withoutRequestID {
				joining = None[protocol.RequestID]()
			}

			decision, err = h.engine.IngressCandidate(test.candidate).ProcessRequest(h.ctx, joining, "compared", notIgnored, unreadPayload(t))

			expected := test.expected
			if !test.withoutRequestID {
				expected = fmt.Sprintf(test.expected, requestID)
			}
			assert.EqualError(t, err, expected)
			assert.Equal(t, nil, decision)
			assert.Equal(t, []string{expected}, h.quarantine.Reasons())
		})
	}
}

func TestIngressDropsReferenceWithoutCandidate(t *testing.T) {
	h := newHarness(t, time.Second)
	decision, err := h.engine.IngressReference(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), "compared", notIgnored, unreadPayload(t))
	assert.NoError(t, err)
	raw := newRaw("reference")

	h.engine.IngressReference(fake).ProcessResponse(h.ctx, decision.(protocol.DecisionForward).Exchange, protocol.Response{Decode: statusOnly("OK"), Raw: raw})

	assert.Equal(t, 1, raw.Releases())
	assert.Equal(t, nil, h.quarantine.Reasons())
	assert.Equal(t, []map[string]any{}, h.records(t, "Response comparison completed"))
}

func TestIngressCandidateWaitsForReference(t *testing.T) {
	h := newHarness(t, time.Second)
	reference, candidate := h.mirror(t, "compared", notIgnored)
	responded := make(chan struct{})
	go func() {
		defer close(responded)
		h.engine.IngressCandidate(fake).ProcessResponse(h.ctx, candidate, protocol.Response{Decode: statusOnly("NOT_FOUND"), Raw: newRaw("candidate")})
	}()
	time.Sleep(10 * time.Millisecond)

	h.engine.IngressReference(fake).ProcessResponse(h.ctx, reference, protocol.Response{Decode: statusOnly("OK"), Raw: newRaw("reference")})
	<-responded

	assert.Equal(t, []string{"response differences: $status"}, h.quarantine.Reasons())
}

func TestIngressIgnoredCandidateReportsWithoutReference(t *testing.T) {
	h := newHarness(t, time.Second)
	reference, candidate := h.mirror(t, "ignored", ignoredFor)
	undecoded := func() protocol.Content {
		t.Error("ignored response was decoded")
		return protocol.Content{}
	}
	referenceRaw, candidateRaw := newRaw("reference"), newRaw("candidate")
	// A candidate that waited for the reference would hit this deadline.
	ctx, cancel := context.WithTimeout(h.ctx, time.Second)
	defer cancel()

	h.engine.IngressCandidate(fake).ProcessResponse(ctx, candidate, protocol.Response{Raw: candidateRaw, Decode: undecoded})

	assert.Equal(t, []map[string]any{comparisonRecord("ignored", map[string]any{"outcome": "skipped", "reason": "ignored for test"})},
		h.records(t, "Response comparison completed"))
	assert.Equal(t, []map[string]any{}, h.records(t, "Response comparison did not finish"))
	assert.Equal(t, 1, candidateRaw.Releases())
	assert.Equal(t, 0, referenceRaw.Releases())

	h.engine.IngressReference(fake).ProcessResponse(h.ctx, reference, protocol.Response{Raw: referenceRaw, Decode: undecoded})

	assert.Equal(t, 1, referenceRaw.Releases())
	assert.Equal(t, nil, h.quarantine.Reasons())
}

func TestIngressCandidateCancelledDuringComparisonDoesNotQuarantine(t *testing.T) {
	h := newHarness(t, time.Second)
	reference, candidate := h.mirror(t, "compared", notIgnored)
	referenceRaw, candidateRaw := newRaw("reference"), newRaw("candidate")
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	interrupted := func() protocol.Content {
		cancel()
		return protocol.Content{Failure: Some(errors.New("read interrupted"))}
	}
	h.engine.IngressReference(fake).ProcessResponse(h.ctx, reference, protocol.Response{Raw: referenceRaw, Decode: statusOnly("OK")})

	h.engine.IngressCandidate(fake).ProcessResponse(ctx, candidate, protocol.Response{Raw: candidateRaw, Decode: interrupted})

	assert.Equal(t, nil, h.quarantine.Reasons())
	assert.Equal(t, []map[string]any{}, h.records(t, "Response comparison completed"))
	assert.Equal(t, []map[string]any{unfinishedRecord("context canceled: candidate: read interrupted")},
		h.records(t, "Response comparison did not finish"))
	assert.Equal(t, 1, referenceRaw.Releases())
	assert.Equal(t, 1, candidateRaw.Releases())
}

func TestIngressCandidateWaitIsBoundedByContext(t *testing.T) {
	h := newHarness(t, time.Second)
	reference, candidate := h.mirror(t, "compared", notIgnored)
	referenceRaw, candidateRaw := newRaw("reference"), newRaw("candidate")
	ctx, cancel := context.WithTimeout(h.ctx, 20*time.Millisecond)
	defer cancel()

	h.engine.IngressCandidate(fake).ProcessResponse(ctx, candidate, protocol.Response{Decode: statusOnly("NOT_FOUND"), Raw: candidateRaw})

	assert.Equal(t, 1, candidateRaw.Releases())
	assert.Equal(t, []map[string]any{unfinishedRecord("context deadline exceeded")}, h.records(t, "Response comparison did not finish"))

	h.engine.IngressReference(fake).ProcessResponse(h.ctx, reference, protocol.Response{Decode: statusOnly("OK"), Raw: referenceRaw})

	assert.Equal(t, 1, referenceRaw.Releases())
	assert.Equal(t, nil, h.quarantine.Reasons())
	assert.Equal(t, []map[string]any{}, h.records(t, "Response comparison completed"))
}

func TestIngressReleasesResponsesForUnknownExchanges(t *testing.T) {
	for name, acceptor := range map[string]func(engine *protocol.Engine) protocol.Acceptor{
		"Reference": func(engine *protocol.Engine) protocol.Acceptor { return engine.IngressReference(fake) },
		"Candidate": func(engine *protocol.Engine) protocol.Acceptor { return engine.IngressCandidate(fake) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, time.Second)
			reference, candidate := h.mirror(t, "compared", notIgnored)
			h.engine.IngressReference(fake).ProcessResponse(h.ctx, reference, protocol.Response{Decode: statusOnly("OK"), Raw: newRaw("reference")})
			h.engine.IngressCandidate(fake).ProcessResponse(h.ctx, candidate, protocol.Response{Decode: statusOnly("OK"), Raw: newRaw("candidate")})
			raw := newRaw("repeated")
			exchange := map[string]protocol.Exchange{"Reference": reference, "Candidate": candidate}[name]

			acceptor(h.engine).ProcessResponse(h.ctx, exchange, protocol.Response{Decode: statusOnly("OK"), Raw: raw})

			assert.Equal(t, 1, raw.Releases())
			assert.Equal(t, []map[string]any{{"level": "ERROR", "msg": "Unknown ingress exchange", "exchange": exchange.String()}},
				h.records(t, "Unknown ingress exchange"))
		})
	}
}

// mirror starts a reference request and joins its mirrored candidate request.
func (h *harness) mirror(t *testing.T, route protocol.Route, ignored Option[string]) (reference, candidate protocol.Exchange) {
	t.Helper()
	decision, err := h.engine.IngressReference(fake).ProcessRequest(h.ctx, None[protocol.RequestID](), route, ignored, unreadPayload(t))
	assert.NoError(t, err)
	referenceForward := decision.(protocol.DecisionForward)
	decision, err = h.engine.IngressCandidate(fake).ProcessRequest(h.ctx, referenceForward.RequestID, route, ignored, unreadPayload(t))
	assert.NoError(t, err)
	candidateForward := decision.(protocol.DecisionForward)
	assert.Equal(t, referenceForward.RequestID, candidateForward.RequestID)
	assert.NotEqual(t, referenceForward.Exchange, candidateForward.Exchange)
	return referenceForward.Exchange, candidateForward.Exchange
}

// unreadPayload fails the test if ingress reads a request payload.
func unreadPayload(t *testing.T) func(ctx context.Context) (protocol.Message, error) {
	return func(context.Context) (protocol.Message, error) {
		t.Error("ingress read a request payload")
		return protocol.Message{}, errors.New("unread")
	}
}

// decoded returns a Decode func for a response with a known status and message.
func decoded(status string, message Option[protocol.Message]) func() protocol.Content {
	return func() protocol.Content { return protocol.Content{Status: Some(status), Message: message} }
}

func statusOnly(status string) func() protocol.Content {
	return decoded(status, None[protocol.Message]())
}

// undecodable returns a Decode func whose body failed to decode.
func undecodable(status Option[string], reason string) func() protocol.Content {
	return func() protocol.Content { return protocol.Content{Status: status, Failure: Some(errors.New(reason))} }
}

func message(typeRef Option[schema.TypeRef], value, trace string) Option[protocol.Message] {
	return Some(protocol.Message{Type: typeRef, Value: map[string]any{"value": value, "trace": trace}})
}

// comparisonRecord is the comparison log record for route with attributes added.
func comparisonRecord(route protocol.Route, attributes map[string]any) map[string]any {
	record := map[string]any{"level": "INFO", "msg": "Response comparison completed", "event": "correlation", "protocol": "fake", "route": string(route)}
	for key, value := range attributes {
		record[key] = value
	}
	return record
}

func unfinishedRecord(reason string) map[string]any {
	return map[string]any{"level": "WARN", "msg": "Response comparison did not finish", "protocol": "fake", "route": "compared", "error": reason}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

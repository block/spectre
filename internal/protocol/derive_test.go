package protocol_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/schema"
)

// deriveScript answers unit lookups from the employments the reference listed.
const deriveScript = `
import * as spectre from "spectre";
import type * as v1 from "people.v1";
const employments = "POST people.example/people.v1.People/ListEmployments";
spectre.egress.http(employments).match<v1.ListEmploymentsRequest>();
spectre.egress
  .http("POST people.example/people.v1.People/GetUnits")
  .derive<v1.GetUnitsRequest, v1.GetUnitsResponse>()
  .from<v1.ListEmploymentsRequest, v1.ListEmploymentsResponse>(
    employments,
    (request) => {
      if (request.person === "unmappable") {
        throw new Error("unmappable");
      }
      return { person: request.person };
    },
    (request, reference) => {
      if (request.employment === "throws") {
        throw new Error("throws");
      }
      if (request.employment === "invalid") {
        return { units: "invalid" } as unknown as v1.GetUnitsResponse;
      }
      if (reference.error === "NOT_FOUND") {
        return { error: "NOT_FOUND", units: [] };
      }
      const employment = reference.employments.find((employment) => employment.token === request.employment);
      return { units: employment?.units ?? [], trace: request.trace };
    },
  );
spectre.field<v1.GetUnitsRequest, "trace">(() => undefined);
spectre.field<v1.ListEmploymentsRequest, "trace">(() => undefined);
`

const (
	unitsRoute       = model.Route("POST people.example/people.v1.People/GetUnits")
	employmentsRoute = model.Route("POST people.example/people.v1.People/ListEmployments")
	derivationName   = `egress.http("POST people.example/people.v1.People/GetUnits").derive().from("POST people.example/people.v1.People/ListEmployments")`
)

func TestNewEngineDeclaresSourceResponseTypes(t *testing.T) {
	matcher := &recordingMatcher{}

	_, err := protocol.NewEngine(t.Context(), newConfig(t, deriveScript, time.Second),
		map[model.Protocol]protocol.Matcher{model.ProtocolHTTP: matcher}, &recordingQuarantiner{})

	assert.NoError(t, err)
	assert.Equal(t, []string{
		`egress endpoint "POST people.example/people.v1.People/ListEmployments" with responses as people.v1.ListEmploymentsResponse`,
		`egress endpoint "POST people.example/people.v1.People/GetUnits"`,
	}, matcher.declared)
}

func TestEgressDerivedEndpointPairsWithItsOwnReference(t *testing.T) {
	t.Parallel()
	h := newScriptHarness(t, deriveScript, time.Second)
	raw := newRaw("units")
	ownReference(t, h, "person-1", "employment-1", raw)

	decision, err := askUnits(h.ctx, h, unitsRequest("employment-1"))

	assert.NoError(t, err)
	assert.Equal(t, protocol.Decision(protocol.DecisionIntercept{Raw: raw}), decision)
	assert.Equal(t, nil, h.quarantine.Reasons())
}

func TestEgressDerivesAnswerFromSourceReference(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		employment string
		listed     map[string]any
		expected   map[string]any
	}{
		"Employment": {
			employment: "employment-1",
			listed:     employed("unit-1", "unit-2"),
			// The normaliser drops trace from the hash, not from the request the answer is derived from.
			expected: map[string]any{"units": []any{"unit-1", "unit-2"}, "trace": "candidate"},
		},
		"UnknownEmployment": {
			employment: "employment-2",
			listed:     employed("unit-1"),
			expected:   map[string]any{"units": []any{}, "trace": "candidate"},
		},
		"PersonNotFound": {
			employment: "employment-1",
			listed:     map[string]any{"error": "NOT_FOUND", "employments": []any{}},
			expected:   map[string]any{"error": "NOT_FOUND", "units": []any{}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newScriptHarness(t, deriveScript, time.Second)
			raw, hold := newRaw("employments"), newRaw("hold")
			sourceReference(t, h, raw, listedEmployments(hold, test.listed))

			decision, err := askUnits(h.ctx, h, unitsRequest(test.employment))

			assert.NoError(t, err)
			assert.Equal(t, answered(test.expected), decision)
			assert.Equal(t, nil, h.quarantine.Reasons())
			assert.Equal(t, []map[string]any{{
				"level": "INFO", "msg": "Candidate request was derived from a reference request", "event": "correlation",
				"protocol": "http", "route": string(unitsRoute), "source": string(employmentsRoute),
			}}, h.records(t, "Candidate request was derived from a reference request"))
			assert.Equal(t, 0, raw.Releases())
			assert.Equal(t, 0, hold.Releases())
		})
	}
}

func TestEgressPrefersOwnReferenceAndKeepsSourceAvailable(t *testing.T) {
	t.Parallel()
	h := newScriptHarness(t, deriveScript, time.Second)
	sourceRaw, hold, ownRaw := newRaw("employments"), newRaw("hold"), newRaw("units")
	sourceReference(t, h, sourceRaw, listedEmployments(hold, employed("unit-1")))
	ownReference(t, h, "person-1", "employment-1", ownRaw)
	derived := answered(map[string]any{"units": []any{"unit-1"}, "trace": "candidate"})

	first, err := askUnits(h.ctx, h, unitsRequest("employment-1"))
	assert.NoError(t, err)
	second, err := askUnits(h.ctx, h, unitsRequest("employment-1"))
	assert.NoError(t, err)
	listed, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), employmentsRoute,
		notIgnored, employmentsRequest("person-1", "candidate"))
	assert.NoError(t, err)
	third, err := askUnits(h.ctx, h, unitsRequest("employment-1"))
	assert.NoError(t, err)

	assert.Equal(t, protocol.Decision(protocol.DecisionIntercept{Raw: ownRaw}), first)
	assert.Equal(t, derived, second)
	assert.Equal(t, protocol.Decision(protocol.DecisionIntercept{Raw: sourceRaw}), listed)
	assert.Equal(t, derived, third)
	assert.Equal(t, nil, h.quarantine.Reasons())
	assert.Equal(t, 0, hold.Releases())
}

func TestEgressDerivedCandidateUsesWhicheverReferenceArrivesFirst(t *testing.T) {
	t.Parallel()
	for name, sourceFirst := range map[string]bool{"Own": false, "Source": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newScriptHarness(t, deriveScript, time.Second)
			sourceRaw, hold, ownRaw := newRaw("employments"), newRaw("hold"), newRaw("units")
			arrive := []func(){
				func() { ownReference(t, h, "person-1", "employment-1", ownRaw) },
				func() { sourceReference(t, h, sourceRaw, listedEmployments(hold, employed("unit-1"))) },
			}
			if sourceFirst {
				arrive[0], arrive[1] = arrive[1], arrive[0]
			}
			decided := candidateUnits(h.ctx, h, unitsRequest("employment-1"))
			time.Sleep(20 * time.Millisecond)

			arrive[0]()
			first := <-decided
			arrive[1]()
			second, err := askUnits(h.ctx, h, unitsRequest("employment-1"))

			assert.NoError(t, first.err)
			assert.NoError(t, err)
			own := protocol.Decision(protocol.DecisionIntercept{Raw: ownRaw})
			derived := answered(map[string]any{"units": []any{"unit-1"}, "trace": "candidate"})
			if sourceFirst {
				// The derivation never claimed the reference to the endpoint itself.
				assert.Equal(t, derived, first.decision)
				assert.Equal(t, own, second)
			} else {
				assert.Equal(t, own, first.decision)
				assert.Equal(t, derived, second)
			}
			assert.Equal(t, nil, h.quarantine.Reasons())
		})
	}
}

func TestEgressDerivedCandidateWaitsOneWindowForEither(t *testing.T) {
	t.Parallel()
	const window = 600 * time.Millisecond
	h := newScriptHarness(t, deriveScript, window)
	started := time.Now()
	decided := candidateUnits(h.ctx, h, unitsRequest("employment-1"))
	time.Sleep(window / 2)

	sourceReference(t, h, newRaw("employments"), listedEmployments(newRaw("hold"), employed("unit-1")))
	result := <-decided

	assert.NoError(t, result.err)
	assert.Equal(t, answered(map[string]any{"units": []any{"unit-1"}, "trace": "candidate"}), result.decision)
	assert.True(t, time.Since(started) < window)
}

func TestEgressDerivedCandidateWithNoReferenceIsUnmatchedAfterOneWindow(t *testing.T) {
	t.Parallel()
	const window = 200 * time.Millisecond
	h := newScriptHarness(t, deriveScript, window)
	started := time.Now()

	decision, err := askUnits(h.ctx, h, unitsRequest("employment-1"))

	assert.EqualError(t, err, "no matching reference request within 200ms")
	assert.Equal(t, nil, decision)
	assert.True(t, time.Since(started) < 2*window)
	assert.Equal(t, []model.QuarantineReason{{
		Kind:    model.QuarantineUnmatched,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: unitsRoute}),
		Detail:  "no matching reference request within 200ms",
	}}, h.quarantine.Recorded())
}

func TestEgressDerivedCandidateGivesUp(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		// selected sends a source reference without its response, so the candidate gives up deriving.
		selected bool
		cause    error
		expected []model.QuarantineKind
	}{
		"WaitingTimedOut":  {cause: errors.New("candidate timed out"), expected: []model.QuarantineKind{model.QuarantineUnmatched}},
		"WaitingStopped":   {cause: protocol.ErrCandidateStopped},
		"DerivingTimedOut": {selected: true, cause: errors.New("candidate timed out")},
		"DerivingStopped":  {selected: true, cause: protocol.ErrCandidateStopped},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			const window = 300 * time.Millisecond
			h := newScriptHarness(t, deriveScript, window)
			raw, hold := newRaw("employments"), newRaw("hold")
			exchange := None[protocol.Exchange]()
			if test.selected {
				exchange = Some(sendReference(t, h, employmentsRoute, employmentsRequest("person-1", "reference")))
			}
			ctx, cancel := context.WithCancelCause(h.ctx)
			decided := candidateUnits(ctx, h, unitsRequest("employment-1"))
			time.Sleep(50 * time.Millisecond)

			cancel(test.cause)
			result := <-decided

			assert.Error(t, result.err)
			assert.Equal(t, nil, result.decision)
			kinds := []model.QuarantineKind{}
			for _, reason := range h.quarantine.Recorded() {
				kinds = append(kinds, reason.Kind)
			}
			assert.Equal(t, append([]model.QuarantineKind{}, test.expected...), kinds)
			pending, ok := exchange.Get()
			if !ok {
				return
			}
			// Giving up leaves the source to be replayed, and its hold to be released once withdrawn.
			respond(t, h, pending, raw, listedEmployments(hold, employed("unit-1")))
			listed, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), employmentsRoute,
				notIgnored, employmentsRequest("person-1", "candidate"))
			assert.NoError(t, err)
			assert.Equal(t, protocol.Decision(protocol.DecisionIntercept{Raw: raw}), listed)
			assert.Equal(t, 0, raw.Releases())
			awaitReleased(t, hold)
		})
	}
}

func TestEgressWithdrawsSourceAfterWindow(t *testing.T) {
	t.Parallel()
	const window = 100 * time.Millisecond
	h := newScriptHarness(t, deriveScript, window)
	raw, hold := newRaw("employments"), newRaw("hold")
	sourceReference(t, h, raw, listedEmployments(hold, employed("unit-1")))

	awaitReleased(t, hold)
	awaitReleased(t, raw)
	_, err := askUnits(h.ctx, h, unitsRequest("employment-1"))

	assert.EqualError(t, err, "no matching reference request within 100ms")
}

func TestEgressKeepsDecodedSourceUntilItsDerivationFinishes(t *testing.T) {
	t.Parallel()
	const window = 100 * time.Millisecond
	h := newScriptHarness(t, deriveScript, window)
	raw, hold := newRaw("employments"), newRaw("hold")
	exchange := sendReference(t, h, employmentsRoute, employmentsRequest("person-1", "reference"))
	decided := candidateUnits(h.ctx, h, unitsRequest("employment-1"))
	// The source is withdrawn while the candidate waits for its response.
	time.Sleep(2 * window)

	respond(t, h, exchange, raw, listedEmployments(hold, employed("unit-1")))
	result := <-decided

	assert.NoError(t, result.err)
	assert.Equal(t, answered(map[string]any{"units": []any{"unit-1"}, "trace": "candidate"}), result.decision)
	awaitReleased(t, hold)
}

func TestEgressDerivesFromEarliestSource(t *testing.T) {
	t.Parallel()
	h := newScriptHarness(t, deriveScript, time.Second)
	sourceReference(t, h, newRaw("first"), listedEmployments(newRaw("first hold"), employed("unit-1")))
	sourceReference(t, h, newRaw("second"), listedEmployments(newRaw("second hold"), employed("unit-2")))

	decision, err := askUnits(h.ctx, h, unitsRequest("employment-1"))

	assert.NoError(t, err)
	assert.Equal(t, answered(map[string]any{"units": []any{"unit-1"}, "trace": "candidate"}), decision)
}

func TestEgressSourceHoldOutlivesItsReplay(t *testing.T) {
	t.Parallel()
	const window = 200 * time.Millisecond
	h := newScriptHarness(t, deriveScript, window)
	raw, hold := newRaw("employments"), newRaw("hold")
	sourceReference(t, h, raw, listedEmployments(hold, employed("unit-1")))

	listed, err := h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), employmentsRoute,
		notIgnored, employmentsRequest("person-1", "candidate"))
	assert.NoError(t, err)
	assert.Equal(t, protocol.Decision(protocol.DecisionIntercept{Raw: raw}), listed)
	// The server releases a recording once its replay finishes.
	raw.Release()
	derived, err := askUnits(h.ctx, h, unitsRequest("employment-1"))

	assert.NoError(t, err)
	assert.Equal(t, answered(map[string]any{"units": []any{"unit-1"}, "trace": "candidate"}), derived)
	assert.Equal(t, 0, hold.Releases())
	awaitReleased(t, hold)
	time.Sleep(window)
	assert.Equal(t, 1, hold.Releases())
	assert.Equal(t, 1, raw.Releases())
}

func TestEgressQuarantinesUnusableDerivations(t *testing.T) {
	t.Parallel()
	failingEncoder := func(context.Context, protocol.Message) (protocol.Raw, error) {
		return nil, errors.New("gRPC requests cannot be answered")
	}
	for name, test := range map[string]struct {
		person, employment string
		encode             Option[protocol.Encoder]
		// source is the source reference's decoded response, or None if the reference failed.
		source   Option[protocol.Content]
		expected string
	}{
		"RequestThrows": {
			person: "unmappable", employment: "employment-1", encode: Some[protocol.Encoder](encodeAnswer),
			source:   Some(listedEmployments(newRaw("hold"), employed("unit-1"))),
			expected: "map candidate request to its source: JavaScript request function of " + derivationName + " threw an exception",
		},
		"ResponseThrows": {
			person: "person-1", employment: "throws", encode: Some[protocol.Encoder](encodeAnswer),
			source:   Some(listedEmployments(newRaw("hold"), employed("unit-1"))),
			expected: "derive response: JavaScript response function of " + derivationName + " threw an exception",
		},
		"InvalidResponse": {
			person: "person-1", employment: "invalid", encode: Some[protocol.Encoder](encodeAnswer),
			source:   Some(listedEmployments(newRaw("hold"), employed("unit-1"))),
			expected: "derive response: derived value is not a valid people.v1.GetUnitsResponse",
		},
		"SourceStatus": {
			person: "person-1", employment: "employment-1", encode: Some[protocol.Encoder](encodeAnswer),
			source:   Some(protocol.Content{Status: Some("404")}),
			expected: `source reference response has status "404"`,
		},
		"SourceUndecodable": {
			person: "person-1", employment: "employment-1", encode: Some[protocol.Encoder](encodeAnswer),
			source:   Some(protocol.Content{Status: Some("200"), Failure: Some(errors.New("body is not JSON"))}),
			expected: "source reference response cannot be decoded: body is not JSON",
		},
		"SourceFailed": {
			person: "person-1", employment: "employment-1", encode: Some[protocol.Encoder](encodeAnswer),
			expected: "source reference response failed: connection reset",
		},
		"EncodingFails": {
			person: "person-1", employment: "employment-1", encode: Some[protocol.Encoder](failingEncoder),
			source:   Some(listedEmployments(newRaw("hold"), employed("unit-1"))),
			expected: "encode derived response: gRPC requests cannot be answered",
		},
		"Unanswerable": {
			person: "person-1", employment: "employment-1", encode: None[protocol.Encoder](),
			source:   Some(listedEmployments(newRaw("hold"), employed("unit-1"))),
			expected: "candidate request cannot be answered with a derived response",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newScriptHarness(t, deriveScript, time.Second)
			exchange := sendReference(t, h, employmentsRoute, employmentsRequest(test.person, "reference"))
			if content, ok := test.source.Get(); ok {
				respond(t, h, exchange, newRaw("employments"), content)
			} else {
				h.engine.EgressReference(model.ProtocolHTTP).Unable(h.ctx, exchange, errors.New("connection reset"))
			}

			_, err := askUnits(h.ctx, h, unitsRequestEncodedBy(test.person, test.employment, test.encode))

			assert.Error(t, err)
			recorded := h.quarantine.Recorded()
			assert.Equal(t, 1, len(recorded), "%v", recorded)
			assert.Equal(t, model.QuarantineUnable, recorded[0].Kind)
			assert.Equal(t, Some(model.Traffic{Direction: model.Egress, Protocol: model.ProtocolHTTP, Route: unitsRoute}), recorded[0].Traffic)
			assert.Contains(t, recorded[0].Detail, test.expected)
		})
	}
}

// answeredRaw is a derived answer, kept as the message it encodes.
type answeredRaw struct {
	message protocol.Message
}

func (*answeredRaw) Release() {}

func encodeAnswer(_ context.Context, answer protocol.Message) (protocol.Raw, error) {
	return &answeredRaw{message: answer}, nil
}

func answered(value map[string]any) protocol.Decision {
	return protocol.DecisionIntercept{Raw: &answeredRaw{message: protocol.Message{
		Type:  Some[schema.TypeRef]("people.v1.GetUnitsResponse"),
		Value: value,
	}}}
}

func unitsRequest(employment string) func(ctx context.Context) (protocol.Message, error) {
	return unitsRequestEncodedBy("person-1", employment, Some[protocol.Encoder](encodeAnswer))
}

// unitsRequestEncodedBy returns a fresh candidate payload on every call, because normalisation mutates it.
func unitsRequestEncodedBy(person, employment string, encode Option[protocol.Encoder]) func(ctx context.Context) (protocol.Message, error) {
	return func(context.Context) (protocol.Message, error) {
		return protocol.Message{
			Type:   Some[schema.TypeRef]("people.v1.GetUnitsRequest"),
			Value:  map[string]any{"person": person, "employment": employment, "trace": "candidate"},
			Encode: encode,
		}, nil
	}
}

func employmentsRequest(person, trace string) func(ctx context.Context) (protocol.Message, error) {
	return func(context.Context) (protocol.Message, error) {
		return protocol.Message{
			Type:  Some[schema.TypeRef]("people.v1.ListEmploymentsRequest"),
			Value: map[string]any{"person": person, "trace": trace},
		}, nil
	}
}

// employed lists one employment, "employment-1", with units.
func employed(units ...string) map[string]any {
	listed := []any{}
	for _, unit := range units {
		listed = append(listed, unit)
	}
	return map[string]any{"employments": []any{map[string]any{"token": "employment-1", "units": listed}}}
}

func listedEmployments(hold *countingRaw, value map[string]any) protocol.Content {
	return protocol.Content{
		Status:  Some("200"),
		Message: Some(protocol.Message{Type: Some[schema.TypeRef]("people.v1.ListEmploymentsResponse"), Value: value}),
		Release: Some(hold.Release),
	}
}

// sendReference forwards an HTTP reference request and waits until it is hashed.
func sendReference(t *testing.T, h *harness, route model.Route, request func(ctx context.Context) (protocol.Message, error)) protocol.Exchange {
	t.Helper()
	decision, err := h.engine.EgressReference(model.ProtocolHTTP).ProcessRequest(h.ctx, None[protocol.RequestID](), route, notIgnored, request)
	assert.NoError(t, err)
	assert.NoError(t, h.engine.Shutdown(h.ctx))
	forward, ok := decision.(protocol.DecisionForward)
	assert.True(t, ok, fmt.Sprintf("%T", decision))
	return forward.Exchange
}

// respond reports a reference response and waits until it is decoded.
func respond(t *testing.T, h *harness, exchange protocol.Exchange, raw *countingRaw, content protocol.Content) {
	t.Helper()
	h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, protocol.Response{
		Raw:    raw,
		Decode: func(context.Context) protocol.Content { return content },
	})
	assert.NoError(t, h.engine.Shutdown(h.ctx))
}

func sourceReference(t *testing.T, h *harness, raw *countingRaw, content protocol.Content) {
	t.Helper()
	respond(t, h, sendReference(t, h, employmentsRoute, employmentsRequest("person-1", "reference")), raw, content)
}

func ownReference(t *testing.T, h *harness, person, employment string, raw *countingRaw) {
	t.Helper()
	exchange := sendReference(t, h, unitsRoute, unitsRequestEncodedBy(person, employment, None[protocol.Encoder]()))
	h.engine.EgressReference(model.ProtocolHTTP).ProcessResponse(h.ctx, exchange, protocol.Response{Raw: raw})
}

func askUnits(ctx context.Context, h *harness, request func(ctx context.Context) (protocol.Message, error)) (protocol.Decision, error) {
	return errors.WithStack2(h.engine.EgressCandidate(model.ProtocolHTTP).ProcessRequest(ctx, None[protocol.RequestID](), unitsRoute, notIgnored, request))
}

func candidateUnits(ctx context.Context, h *harness, request func(ctx context.Context) (protocol.Message, error)) <-chan candidateDecision {
	decided := make(chan candidateDecision, 1)
	go func() {
		decision, err := askUnits(ctx, h, request)
		decided <- candidateDecision{decision: decision, err: err}
	}()
	return decided
}

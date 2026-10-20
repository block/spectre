package protocol

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/future"
	"github.com/block/spectre/internal/logger"
)

// outcome classifies one response comparison.
type outcome string

const (
	outcomeEquivalent outcome = "equivalent"
	outcomeDivergent  outcome = "divergent"
	// outcomeSkipped means the responses are never compared, so never quarantine.
	outcomeSkipped outcome = "skipped"
	outcomeUnable  outcome = "unable"
)

// comparisonResult describes a comparison without exposing response values.
type comparisonResult struct {
	outcome     outcome
	differences []string
	reason      string
}

func newComparisonResult(outcome outcome, reason string) comparisonResult {
	return comparisonResult{outcome: outcome, reason: reason}
}

func newDifferenceResult(differences ...string) comparisonResult {
	return comparisonResult{outcome: outcomeDivergent, differences: differences}
}

// ingressPairs pairs each reference request with its mirrored candidate request
// by RequestID, then compares their responses.
type ingressPairs struct {
	scripts    *comparison.Scripts
	quarantine Quarantiner

	// mu guards both maps and every pair's lifecycle flags.
	mu        sync.Mutex
	requests  map[RequestID]*ingressPair
	exchanges map[Exchange]*ingressPair
}

func newIngressPairs(scripts *comparison.Scripts, quarantine Quarantiner) *ingressPairs {
	return &ingressPairs{
		scripts:    scripts,
		quarantine: quarantine,
		requests:   map[RequestID]*ingressPair{},
		exchanges:  map[Exchange]*ingressPair{},
	}
}

// ingressOutcome is how a reference request finished.
type ingressOutcome struct {
	reference Option[Response]
	failure   Option[error]
	// cancelled means the reference failed because it was cancelled, so its
	// response is unknown rather than wrong.
	cancelled bool
}

// ingressPair is one mirrored request. ingressPairs.mu guards its lifecycle flags.
type ingressPair struct {
	protocol  boundary.Protocol
	route     Route
	requestID RequestID
	// ignoredReason explains why the responses are never compared.
	ignoredReason Option[string]
	outcome       *future.Future[ingressOutcome]

	joined        bool
	candidateDone bool
}

func newIngressPair(protocol boundary.Protocol, requestID RequestID, route Route, ignored Option[string]) *ingressPair {
	return &ingressPair{
		protocol: protocol, route: route, requestID: requestID, ignoredReason: ignored,
		outcome: future.New[ingressOutcome](),
	}
}

func (p *ingressPair) id() RequestID {
	return p.requestID
}

// describe returns the log attributes that identify the pair.
func (p *ingressPair) describe() []any {
	return []any{"protocol", p.protocol, "route", p.route}
}

// ignored returns why the pair is never compared, if it is not.
func (p *ingressPair) ignored() Option[string] {
	return p.ignoredReason
}

// join admits the pair's only candidate.
func (p *ingressPair) join(protocol boundary.Protocol) error {
	if p.joined {
		return errors.Errorf("reference request %s already has a candidate", p.requestID)
	}
	if p.protocol != protocol {
		return errors.Errorf("candidate request %s uses %s, not %s", p.requestID, protocol, p.protocol)
	}
	p.joined = true
	return nil
}

// finishReference publishes the reference outcome and reports whether the pair
// is finished, because no candidate joined or the candidate already finished.
func (p *ingressPair) finishReference(outcome ingressOutcome) (finished bool) {
	if !p.outcome.Set(outcome) {
		// The pair already has an outcome, so this response is nobody's.
		releaseResponse(outcome.reference)
		return false
	}
	finished = p.candidateDone || !p.joined
	if finished {
		p.releaseReference()
	}
	return finished
}

func (p *ingressPair) finishCandidate() {
	p.candidateDone = true
	p.releaseReference()
}

// releaseReference frees the reference response, if there is one yet, once
// neither side needs it.
func (p *ingressPair) releaseReference() {
	if outcome, finished := p.outcome.Get(); finished {
		releaseResponse(outcome.reference)
	}
}

func releaseResponse(response Option[Response]) {
	if response, ok := response.Get(); ok {
		response.Raw.Release()
	}
}

// awaitReference waits for the reference outcome. An error means the outcome
// is unknown, so the responses cannot be compared.
func (p *ingressPair) awaitReference(ctx context.Context) (ingressOutcome, error) {
	outcome, err := p.outcome.Wait(ctx)
	if err != nil {
		return ingressOutcome{}, errors.WithStack(err)
	}
	if failure, failed := outcome.failure.Get(); failed && outcome.cancelled {
		return ingressOutcome{}, errors.WithStack(failure)
	}
	return outcome, nil
}

// compareResponses classifies a finished pair without exposing response values. Known
// statuses are compared first, so a status change is divergent even if a body fails to decode.
func compareResponses(ctx context.Context, scripts *comparison.Scripts, outcome ingressOutcome, candidate Response) comparisonResult {
	if failure, failed := outcome.failure.Get(); failed {
		return newComparisonResult(outcomeUnable, "reference: "+failure.Error())
	}
	reference, ok := outcome.reference.Get()
	if !ok {
		return newComparisonResult(outcomeUnable, "reference response is missing")
	}
	referenceContent, candidateContent := reference.Decode(), candidate.Decode()
	referenceStatus, referenceKnown := referenceContent.Status.Get()
	candidateStatus, candidateKnown := candidateContent.Status.Get()
	if referenceKnown && candidateKnown && referenceStatus != candidateStatus {
		return newDifferenceResult("$status")
	}
	if failure, failed := referenceContent.Failure.Get(); failed {
		return newComparisonResult(outcomeUnable, "reference: "+failure.Error())
	}
	if failure, failed := candidateContent.Failure.Get(); failed {
		return newComparisonResult(outcomeUnable, "candidate: "+failure.Error())
	}
	if !referenceKnown || !candidateKnown {
		return newComparisonResult(outcomeUnable, "response status is unknown")
	}
	return compareMessages(ctx, scripts, referenceContent.Message, candidateContent.Message)
}

func compareMessages(ctx context.Context, scripts *comparison.Scripts, reference, candidate Option[Message]) comparisonResult {
	referenceMessage, referenceOK := reference.Get()
	candidateMessage, candidateOK := candidate.Get()
	switch {
	case !referenceOK && !candidateOK:
		return newComparisonResult(outcomeEquivalent, "")
	case referenceOK != candidateOK:
		return newDifferenceResult("$")
	case referenceMessage.Type != candidateMessage.Type:
		return newComparisonResult(outcomeUnable, "reference and candidate messages have different types")
	}
	differences, err := scripts.Compare(ctx, referenceMessage.Type, referenceMessage.Value, candidateMessage.Value)
	if err != nil {
		return newComparisonResult(outcomeUnable, err.Error())
	}
	if len(differences) > 0 {
		return newDifferenceResult(differences...)
	}
	return newComparisonResult(outcomeEquivalent, "")
}

// latencyDelta returns the candidate's latency minus the reference's, if both are known.
func (p *ingressPair) latencyDelta(candidate time.Duration) Option[time.Duration] {
	if candidate <= 0 {
		return None[time.Duration]()
	}
	outcome, finished := p.outcome.Get()
	if !finished {
		return None[time.Duration]()
	}
	if reference, ok := outcome.reference.Get(); ok && reference.Latency > 0 {
		return Some(candidate - reference.Latency)
	}
	return None[time.Duration]()
}

// start registers a reference request under a new RequestID.
func (p *ingressPairs) start(protocol boundary.Protocol, route Route, ignored Option[string]) Decision {
	requestID, exchange := newRequestID(), newExchange()
	pair := newIngressPair(protocol, requestID, route, ignored)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests[requestID] = pair
	p.exchanges[exchange] = pair
	return DecisionForward{RequestID: Some(requestID), Exchange: exchange}
}

// join pairs a candidate request with its reference, quarantining it if it cannot.
func (p *ingressPairs) join(ctx context.Context, protocol boundary.Protocol, requestID Option[RequestID]) (Decision, error) {
	exchange, err := p.admit(protocol, requestID)
	if err != nil {
		p.quarantine.Quarantine(ctx, err)
		return nil, err
	}
	return DecisionForward{RequestID: requestID, Exchange: exchange}, nil
}

func (p *ingressPairs) admit(protocol boundary.Protocol, requestID Option[RequestID]) (Exchange, error) {
	id, ok := requestID.Get()
	if !ok {
		return Exchange{}, errors.New("candidate request has no request ID")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pair, found := p.requests[id]
	if !found {
		return Exchange{}, errors.Errorf("candidate request %s has no reference request", id)
	}
	if err := pair.join(protocol); err != nil {
		return Exchange{}, err
	}
	exchange := newExchange()
	p.exchanges[exchange] = pair
	return exchange, nil
}

// take removes an exchange, logging one the engine never issued.
func (p *ingressPairs) take(ctx context.Context, exchange Exchange) (*ingressPair, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pair, ok := p.exchanges[exchange]
	if !ok {
		logger.FromContext(ctx).ErrorContext(ctx, "Unknown ingress exchange", "exchange", exchange.String())
		return nil, false
	}
	delete(p.exchanges, exchange)
	return pair, true
}

// finishReference publishes the reference outcome. Servers send the candidate
// request before the reference, so a pair no candidate has joined is dropped.
func (p *ingressPairs) finishReference(ctx context.Context, exchange Exchange, response Option[Response], failure Option[error]) {
	pair, ok := p.take(ctx, exchange)
	if !ok {
		if unknown, present := response.Get(); present {
			unknown.Raw.Release()
		}
		return
	}
	outcome := ingressOutcome{reference: response, failure: failure, cancelled: failure.Ok() && ctx.Err() != nil}
	p.mu.Lock()
	defer p.mu.Unlock()
	if pair.finishReference(outcome) {
		delete(p.requests, pair.id())
	}
}

// finishCandidate drops the pair once the candidate has finished with it.
func (p *ingressPairs) finishCandidate(pair *ingressPair) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pair.finishCandidate()
	delete(p.requests, pair.id())
}

// candidateResponse waits for the reference response, bounded by ctx, so
// waiting and decoding run on the candidate's time.
func (p *ingressPairs) candidateResponse(ctx context.Context, exchange Exchange, response Response) {
	defer response.Raw.Release()
	pair, ok := p.take(ctx, exchange)
	if !ok {
		return
	}
	defer p.finishCandidate(pair)
	if reason, ignored := pair.ignored().Get(); ignored {
		p.report(ctx, pair, newComparisonResult(outcomeSkipped, reason), 0)
		return
	}
	reference, err := pair.awaitReference(ctx)
	if err != nil {
		p.unfinished(ctx, pair, err)
		return
	}
	result := compareResponses(ctx, p.scripts, reference, response)
	// A comparison cut short by the candidate's own deadline says nothing about it.
	if result.outcome == outcomeUnable && ctx.Err() != nil {
		p.unfinished(ctx, pair, errors.Errorf("%s: %s", ctx.Err(), result.reason))
		return
	}
	p.report(ctx, pair, result, response.Latency)
}

func (p *ingressPairs) unfinished(ctx context.Context, pair *ingressPair, err error) {
	logger.FromContext(ctx).WarnContext(context.WithoutCancel(ctx), "Response comparison did not finish", append(pair.describe(), "error", err)...)
}

func (p *ingressPairs) candidateUnable(ctx context.Context, exchange Exchange, reason error) {
	pair, ok := p.take(ctx, exchange)
	if !ok {
		return
	}
	defer p.finishCandidate(pair)
	if ctx.Err() != nil {
		return
	}
	result := newComparisonResult(outcomeUnable, "candidate: "+reason.Error())
	if ignoredReason, ignored := pair.ignored().Get(); ignored {
		result = newComparisonResult(outcomeSkipped, ignoredReason)
	}
	p.report(ctx, pair, result, 0)
}

// report logs a comparison and applies the fail-closed policy to it.
func (p *ingressPairs) report(ctx context.Context, pair *ingressPair, result comparisonResult, latency time.Duration) {
	attributes := append([]any{"event", logger.EventCorrelation}, pair.describe()...)
	attributes = append(attributes, "outcome", result.outcome)
	if len(result.differences) > 0 {
		attributes = append(attributes, "differences", result.differences)
	}
	if result.reason != "" {
		attributes = append(attributes, "reason", result.reason)
	}
	if dt, ok := pair.latencyDelta(latency).Get(); ok {
		attributes = append(attributes, "dt", dt)
	}
	logger.FromContext(ctx).InfoContext(ctx, "Response comparison completed", attributes...)
	switch result.outcome {
	case outcomeEquivalent, outcomeSkipped:
	case outcomeDivergent:
		p.quarantine.Quarantine(ctx, errors.Errorf("response differences: %s", strings.Join(result.differences, ", ")))
	case outcomeUnable:
		p.quarantine.Quarantine(ctx, errors.Errorf("response comparison failed: %s", result.reason))
	}
}

// ingressReference assigns each inbound request the RequestID its mirror carries.
type ingressReference struct {
	pairs    *ingressPairs
	protocol boundary.Protocol
}

func newIngressReference(pairs *ingressPairs, protocol boundary.Protocol) *ingressReference {
	return &ingressReference{pairs: pairs, protocol: protocol}
}

func (r *ingressReference) ProcessRequest(_ context.Context, _ Option[RequestID], route Route, ignored Option[string],
	_ func(ctx context.Context) (Message, error),
) (Decision, error) {
	return r.pairs.start(r.protocol, route, ignored), nil
}

// ProcessResponse stores the reference response without waiting for the candidate.
func (r *ingressReference) ProcessResponse(ctx context.Context, exchange Exchange, response Response) {
	r.pairs.finishReference(ctx, exchange, Some(response), None[error]())
}

func (r *ingressReference) Unable(ctx context.Context, exchange Exchange, reason error) {
	r.pairs.finishReference(ctx, exchange, None[Response](), Some(reason))
}

// ingressCandidate compares each mirrored response with its reference response.
type ingressCandidate struct {
	pairs    *ingressPairs
	protocol boundary.Protocol
}

func newIngressCandidate(pairs *ingressPairs, protocol boundary.Protocol) *ingressCandidate {
	return &ingressCandidate{pairs: pairs, protocol: protocol}
}

func (c *ingressCandidate) ProcessRequest(ctx context.Context, requestID Option[RequestID], _ Route, _ Option[string],
	_ func(ctx context.Context) (Message, error),
) (Decision, error) {
	return c.pairs.join(ctx, c.protocol, requestID)
}

func (c *ingressCandidate) ProcessResponse(ctx context.Context, exchange Exchange, response Response) {
	c.pairs.candidateResponse(ctx, exchange, response)
}

func (c *ingressCandidate) Unable(ctx context.Context, exchange Exchange, reason error) {
	c.pairs.candidateUnable(ctx, exchange, reason)
}

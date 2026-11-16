package protocol

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/future"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// endpoint identifies an egress endpoint by its declared pattern across protocols.
type endpoint struct {
	protocol model.Protocol
	pattern  string
}

// egressQueue pairs candidate and reference requests of the same hash in arrival
// order. Each reference is claimed once, so duplicates pair one-to-one.
type egressQueue struct {
	scripts    *comparison.Scripts
	window     time.Duration
	quarantine Quarantiner
	// replacements answers candidate requests to an endpoint from the source it replaces,
	// and sourceResponses types the reference responses replacements read.
	replacements    map[endpoint]javascript.Replacement
	sourceResponses map[endpoint]schema.TypeRef
	// optional holds the endpoints whose reference requests the candidate may leave out.
	optional map[endpoint]bool
	// mu guards every map. A hash never has unused references and waiting
	// candidates at the same time, because add hands over to a waiter first.
	mu sync.Mutex
	// arrivals numbers requests as they arrive, because hashing may finish out of order.
	arrivals uint64
	// hashing counts reference hashes, which may finish after their request.
	// idle is closed whenever hashing is zero, so shutdown can wait as hashes start.
	hashing   int
	idle      chan struct{}
	exchanges map[Exchange]*egressExchange
	unused    map[comparison.Hash][]*egressExchange
	waiting   map[comparison.Hash][]*egressWaiter
	// sources keeps source references for the window, whether or not a candidate claims them.
	sources map[comparison.Hash][]*egressExchange
	// replacing holds waiters that also accept a source reference, by source hash. add hands each
	// one reference and removes it from both maps under mu, so a match is never decided late.
	replacing map[comparison.Hash][]*egressWaiter
	// closed means the server is stopping candidate work, so references that
	// expire unclaimed are no longer the candidate's fault.
	closed bool
}

func newEgressQueue(scripts *comparison.Scripts, window time.Duration, quarantine Quarantiner) *egressQueue {
	replacements := map[endpoint]javascript.Replacement{}
	sourceResponses := map[endpoint]schema.TypeRef{}
	for _, replacement := range scripts.Replacements() {
		replacements[endpoint{replacement.Protocol(), replacement.Pattern()}] = replacement
		sourceResponses[endpoint{replacement.Protocol(), replacement.Source()}] = replacement.SourceResponse()
	}
	optional := map[endpoint]bool{}
	for _, declared := range scripts.Endpoints() {
		if declared.Direction() == model.Egress && declared.Optional() {
			optional[endpoint{declared.Protocol(), declared.Pattern()}] = true
		}
	}
	return &egressQueue{
		scripts:         scripts,
		window:          window,
		quarantine:      quarantine,
		replacements:    replacements,
		sourceResponses: sourceResponses,
		optional:        optional,
		exchanges:       map[Exchange]*egressExchange{},
		unused:          map[comparison.Hash][]*egressExchange{},
		waiting:         map[comparison.Hash][]*egressWaiter{},
		sources:         map[comparison.Hash][]*egressExchange{},
		replacing:       map[comparison.Hash][]*egressWaiter{},
		idle:            closedChannel(),
	}
}

// sourceResponse returns the type a replacement reads an endpoint's reference responses as, if any does.
func (q *egressQueue) sourceResponse(protocol model.Protocol, route model.Route) Option[schema.TypeRef] {
	typeName, ok := q.sourceResponses[endpoint{protocol, string(route)}]
	if !ok {
		return None[schema.TypeRef]()
	}
	return Some(typeName)
}

func closedChannel() chan struct{} {
	idle := make(chan struct{})
	close(idle)
	return idle
}

// startHashing records a hash in progress. Callers hold q.mu.
func (q *egressQueue) startHashing() {
	if q.hashing == 0 {
		q.idle = make(chan struct{})
	}
	q.hashing++
}

func (q *egressQueue) finishHashing() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.hashing--
	if q.hashing == 0 {
		close(q.idle)
	}
}

func (q *egressQueue) arrive() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.arrivals++
	return q.arrivals
}

// close stops quarantining for references that expire unclaimed.
func (q *egressQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
}

// shutdown waits until no reference is being hashed.
func (q *egressQueue) shutdown(ctx context.Context) error {
	q.mu.Lock()
	idle := q.idle
	q.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "wait for reference hashing")
	}
}

// hash identifies a request by its protocol, route, and normalised payload.
func (q *egressQueue) hash(ctx context.Context, protocol model.Protocol, route model.Route, side string,
	request func(ctx context.Context) (Message, error),
) (comparison.Hash, error) {
	message, err := request(ctx)
	if err != nil {
		return comparison.Hash{}, errors.WithStack(err)
	}
	return errors.WithStack2(q.scripts.Hash(ctx, string(protocol)+" "+string(route), message.Type, side, message.Value))
}

// forward registers a reference request and hashes it in the background.
func (q *egressQueue) forward(ctx context.Context, protocol model.Protocol, route model.Route,
	request func(ctx context.Context) (Message, error),
) Decision {
	exchange := newExchange()
	q.mu.Lock()
	q.arrivals++
	declared := endpoint{protocol, string(route)}
	_, source := q.sourceResponses[declared]
	pending := newEgressExchange(q.arrivals, protocol, route, source, q.optional[declared])
	q.exchanges[exchange] = pending
	q.startHashing()
	q.mu.Unlock()
	background := context.WithoutCancel(ctx)
	go func() {
		defer q.finishHashing()
		hash, err := q.hash(background, protocol, route, "reference", request)
		if err != nil {
			pending.abandon()
			pending.forget()
			q.quarantine.Quarantine(background, egressReason(model.QuarantineUnable, protocol, route, errors.Wrap(err, "identify reference request")))
			return
		}
		q.add(background, hash, pending)
	}()
	return DecisionForward{RequestID: None[RequestID](), Exchange: exchange}
}

// intercept answers a candidate request with an equivalent reference response.
func (q *egressQueue) intercept(ctx context.Context, protocol model.Protocol, route model.Route,
	request func(ctx context.Context) (Message, error),
) (Decision, error) {
	if replacement, replaces := q.replacements[endpoint{protocol, string(route)}]; replaces {
		return q.answer(ctx, protocol, route, replacement, request)
	}
	raw, kind, err := q.match(ctx, protocol, route, request)
	if err != nil {
		q.refuse(ctx, kind, protocol, route, err)
		return nil, err
	}
	logger.FromContext(ctx).InfoContext(ctx, "Candidate request matched a reference request",
		"event", logger.EventCorrelation, "protocol", protocol, "route", route)
	return DecisionIntercept{Raw: raw}, nil
}

// match returns the reference response to replay, or why there is none.
func (q *egressQueue) match(ctx context.Context, protocol model.Protocol, route model.Route,
	request func(ctx context.Context) (Message, error),
) (Raw, model.QuarantineKind, error) {
	arrival := q.arrive()
	hash, err := q.hash(ctx, protocol, route, "candidate", request)
	if err != nil {
		return nil, model.QuarantineUnable, errors.Wrap(err, "identify candidate request")
	}
	pending, err := q.claim(ctx, arrival, hash)
	if err != nil {
		return nil, model.QuarantineUnmatched, err
	}
	raw, err := pending.wait(ctx)
	if err != nil {
		return nil, model.QuarantineUnable, err
	}
	return raw, "", nil
}

// refuse quarantines the candidate for a request that cannot be answered, unless
// the reference cancelled the request it matched or the candidate is not to blame.
func (q *egressQueue) refuse(ctx context.Context, kind model.QuarantineKind, protocol model.Protocol, route model.Route, err error) {
	switch {
	case errors.Is(err, errReferenceCancelled):
		logger.FromContext(ctx).InfoContext(ctx, "Candidate request matched a cancelled reference request",
			"event", logger.EventCorrelation, "protocol", protocol, "route", route)
	case blamesCandidate(ctx):
		q.quarantine.Quarantine(ctx, egressReason(kind, protocol, route, err))
	}
}

// blamesCandidate reports whether a failed match quarantines the candidate. A candidate that
// gives up was never answered as the reference was, so only work the proxy stopped is excused.
func blamesCandidate(ctx context.Context) bool {
	return !errors.Is(context.Cause(ctx), ErrCandidateStopped)
}

// answer answers a candidate request to a replacing endpoint with an equivalent reference
// request to the endpoint, or else with a response translated from a source reference.
func (q *egressQueue) answer(ctx context.Context, protocol model.Protocol, route model.Route, replacement javascript.Replacement,
	request func(ctx context.Context) (Message, error),
) (Decision, error) {
	decision, kind, err := q.respond(ctx, protocol, route, replacement, request)
	if err != nil {
		q.refuse(ctx, kind, protocol, route, err)
		return nil, err
	}
	if _, intercepted := decision.(DecisionIntercept); intercepted {
		logger.FromContext(ctx).InfoContext(ctx, "Candidate request matched a reference request",
			"event", logger.EventCorrelation, "protocol", protocol, "route", route)
		return decision, nil
	}
	logger.FromContext(ctx).InfoContext(ctx, "Candidate request was answered from a reference response",
		"event", logger.EventCorrelation, "protocol", protocol, "route", route, "source", replacement.Source())
	return decision, nil
}

// respond returns the decision for a candidate request to a replacing endpoint, or why there is none.
func (q *egressQueue) respond(ctx context.Context, protocol model.Protocol, route model.Route, replacement javascript.Replacement,
	request func(ctx context.Context) (Message, error),
) (Decision, model.QuarantineKind, error) {
	arrival := q.arrive()
	hash, value, err := q.identify(ctx, protocol, route, request)
	if err != nil {
		return nil, model.QuarantineUnable, err
	}
	sourceRequest, err := q.scripts.ReplaceRequest(ctx, replacement, value)
	if err != nil {
		return nil, model.QuarantineUnable, errors.Wrap(err, "map candidate request to a source request")
	}
	sourceHash, err := q.scripts.Hash(ctx, string(protocol)+" "+replacement.Source(), Some(replacement.SourceRequest()), "candidate", sourceRequest)
	if err != nil {
		return nil, model.QuarantineUnable, errors.Wrap(err, "identify source request")
	}
	delivery, err := q.pick(ctx, arrival, hash, sourceHash)
	if err != nil {
		return nil, model.QuarantineUnmatched, err
	}
	if delivery.direct {
		raw, err := delivery.pending.wait(ctx)
		if err != nil {
			return nil, model.QuarantineUnable, err
		}
		return DecisionIntercept{Raw: raw}, "", nil
	}
	reference, err := delivery.pending.decoded(ctx)
	if err != nil {
		return nil, model.QuarantineUnable, err
	}
	answer, err := q.scripts.ReplaceResponse(ctx, replacement, value, reference)
	if err != nil {
		return nil, model.QuarantineUnable, errors.Wrap(err, "translate response")
	}
	return DecisionAnswer{Message: Message{Type: Some(replacement.Response()), Value: answer}}, "", nil
}

// identify hashes a candidate request to a replacing endpoint, and returns a copy of its value to
// translate. Hashing normalises the value in place, and replacement functions see only its JSON.
func (q *egressQueue) identify(ctx context.Context, protocol model.Protocol, route model.Route,
	request func(ctx context.Context) (Message, error),
) (comparison.Hash, any, error) {
	message, err := request(ctx)
	if err != nil {
		return comparison.Hash{}, nil, errors.Wrap(err, "identify candidate request")
	}
	encoded, err := json.Marshal(message.Value)
	if err != nil {
		return comparison.Hash{}, nil, errors.Wrap(err, "copy candidate request")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return comparison.Hash{}, nil, errors.Wrap(err, "copy candidate request")
	}
	hash, err := q.scripts.Hash(ctx, string(protocol)+" "+string(route), message.Type, "candidate", message.Value)
	if err != nil {
		return comparison.Hash{}, nil, errors.Wrap(err, "identify candidate request")
	}
	return hash, value, nil
}

func egressReason(kind model.QuarantineKind, protocol model.Protocol, route model.Route, err error) model.QuarantineReason {
	return model.QuarantineReason{
		Kind:    kind,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: protocol, Route: route}),
		Detail:  err.Error(),
	}
}

// misreported logs a candidate response, which is a server error because
// candidate requests are never forwarded.
func (q *egressQueue) misreported(ctx context.Context, exchange Exchange, failure Option[error]) {
	if reason, failed := failure.Get(); failed {
		logger.FromContext(ctx).ErrorContext(ctx, "Egress candidate reported a failed response", "exchange", exchange.String(), "error", reason)
		return
	}
	logger.FromContext(ctx).ErrorContext(ctx, "Egress candidate reported a response", "exchange", exchange.String())
}

// take removes an exchange, logging one the engine never issued.
func (q *egressQueue) take(ctx context.Context, exchange Exchange) (*egressExchange, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	pending, ok := q.exchanges[exchange]
	if !ok {
		logger.FromContext(ctx).ErrorContext(ctx, "Unknown egress exchange", "exchange", exchange.String())
		return nil, false
	}
	delete(q.exchanges, exchange)
	return pending, true
}

// add offers a reference to the earliest waiting candidate, or keeps it unused
// for the window before expiring it.
func (q *egressQueue) add(ctx context.Context, hash comparison.Hash, pending *egressExchange) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if pending.isSource() {
		// Every replacing waiter receives a source reference, and later ones find it for the window.
		replacing := slices.Clone(q.replacing[hash])
		for _, waiter := range replacing {
			q.unregister(waiter)
			waiter.deliver(egressDelivery{pending: pending})
		}
		if len(replacing) > 0 {
			pending.markMatched()
		}
		q.sources[hash] = insertByArrival(q.sources[hash], pending)
		time.AfterFunc(q.window, func() { q.forget(hash, pending) })
	}
	if waiters := q.waiting[hash]; len(waiters) > 0 {
		waiter := waiters[0]
		q.unregister(waiter)
		waiter.deliver(egressDelivery{pending: pending, direct: true})
		return
	}
	q.unused[hash] = insertByArrival(q.unused[hash], pending)
	pending.expireAfter(q.window, func() { q.expire(ctx, hash, pending) })
}

// forget stops offering a source reference to replacing requests.
func (q *egressQueue) forget(hash comparison.Hash, pending *egressExchange) {
	pending.forget()
	q.mu.Lock()
	defer q.mu.Unlock()
	sources := q.sources[hash]
	if index := slices.Index(sources, pending); index >= 0 {
		sources = slices.Delete(sources, index, index+1)
	}
	if len(sources) == 0 {
		delete(q.sources, hash)
		return
	}
	q.sources[hash] = sources
}

// pick waits up to the window for the earliest unused reference with hash, which it claims and
// reports as direct, or else the earliest source reference with sourceHash, which it never claims.
func (q *egressQueue) pick(ctx context.Context, arrival uint64, hash, sourceHash comparison.Hash) (egressDelivery, error) {
	q.mu.Lock()
	if unused := q.unused[hash]; len(unused) > 0 {
		q.setUnused(hash, unused[1:])
		unused[0].stopExpiry()
		q.mu.Unlock()
		return egressDelivery{pending: unused[0], direct: true}, nil
	}
	if sources := q.sources[sourceHash]; len(sources) > 0 {
		// forget edits the slice in place, so read it before unlocking.
		source := sources[0]
		source.markMatched()
		q.mu.Unlock()
		return egressDelivery{pending: source}, nil
	}
	waiter := newEgressWaiter(arrival, hash, Some(sourceHash))
	q.waiting[hash] = insertByArrival(q.waiting[hash], waiter)
	q.replacing[sourceHash] = append(q.replacing[sourceHash], waiter)
	q.mu.Unlock()
	return q.await(ctx, waiter, "no matching reference or source reference request")
}

// claim takes the earliest unused reference for hash, waiting up to the window
// for one to arrive. Ordering covers only hashed references, so it never waits on one still hashing.
func (q *egressQueue) claim(ctx context.Context, arrival uint64, hash comparison.Hash) (*egressExchange, error) {
	q.mu.Lock()
	if unused := q.unused[hash]; len(unused) > 0 {
		q.setUnused(hash, unused[1:])
		unused[0].stopExpiry()
		q.mu.Unlock()
		return unused[0], nil
	}
	waiter := newEgressWaiter(arrival, hash, None[comparison.Hash]())
	q.waiting[hash] = insertByArrival(q.waiting[hash], waiter)
	q.mu.Unlock()
	delivery, err := q.await(ctx, waiter, "no matching reference request")
	return delivery.pending, err
}

// await waits up to the window for add to hand waiter a reference, or else unregisters it.
// missing describes the reference that never arrived.
func (q *egressQueue) await(ctx context.Context, waiter *egressWaiter, missing string) (egressDelivery, error) {
	started := time.Now()
	timer := time.NewTimer(q.window)
	defer timer.Stop()
	var err error
	select {
	case delivery := <-waiter.delivered():
		return delivery, nil
	case <-timer.C:
		err = errors.Errorf("%s within %s", missing, q.window)
	case <-ctx.Done():
		err = errors.Wrapf(ctx.Err(), "%s before the candidate gave up after %s", missing, time.Since(started).Round(time.Millisecond))
	}
	q.mu.Lock()
	waiting := q.unregister(waiter)
	q.mu.Unlock()
	if !waiting {
		// add unregistered this waiter concurrently, so a reference is already on its way.
		return <-waiter.delivered(), nil
	}
	return egressDelivery{}, err
}

// unregister removes a waiter from every queue it waits in, reporting whether
// it was still waiting. Callers hold q.mu.
func (q *egressQueue) unregister(waiter *egressWaiter) (waiting bool) {
	hash, source := waiter.hashes()
	waiting = removeWaiter(q.waiting, hash, waiter)
	if sourceHash, replacing := source.Get(); replacing {
		removeWaiter(q.replacing, sourceHash, waiter)
	}
	return waiting
}

// removeWaiter removes a waiter from the queue for hash, reporting whether it was there.
func removeWaiter(queues map[comparison.Hash][]*egressWaiter, hash comparison.Hash, waiter *egressWaiter) (removed bool) {
	waiters := queues[hash]
	index := slices.Index(waiters, waiter)
	if index < 0 {
		return false
	}
	waiters = slices.Delete(waiters, index, index+1)
	if len(waiters) == 0 {
		delete(queues, hash)
		return true
	}
	queues[hash] = waiters
	return true
}

// expire releases a reference that no candidate claimed within the window. The
// candidate left it out, unless a replacing request matched it.
func (q *egressQueue) expire(ctx context.Context, hash comparison.Hash, pending *egressExchange) {
	q.mu.Lock()
	unused := q.unused[hash]
	index := slices.Index(unused, pending)
	if index >= 0 {
		q.setUnused(hash, slices.Delete(unused, index, index+1))
	}
	leftOut := index >= 0 && !pending.isMatched() && !q.closed
	q.mu.Unlock()
	if index < 0 {
		return
	}
	if leftOut {
		pending.leaveOut(ctx, q.quarantine, q.window)
	}
	pending.abandon()
}

// setUnused replaces the unused queue for hash. Callers hold q.mu.
func (q *egressQueue) setUnused(hash comparison.Hash, unused []*egressExchange) {
	if len(unused) == 0 {
		delete(q.unused, hash)
		return
	}
	q.unused[hash] = unused
}

// insertByArrival keeps a queue in arrival order.
func insertByArrival[T interface{ arrived() uint64 }](queue []T, item T) []T {
	index, _ := slices.BinarySearchFunc(queue, item.arrived(), func(queued T, arrival uint64) int {
		return cmp.Compare(queued.arrived(), arrival)
	})
	return slices.Insert(queue, index, item)
}

// egressDelivery is the one reference add hands a waiter. A direct reference is claimed,
// and a source reference is only matched.
type egressDelivery struct {
	pending *egressExchange
	direct  bool
}

// egressWaiter is a candidate waiting for a matching reference. One to a replacing endpoint
// also accepts a source reference with source, and receives whichever add hands over first.
type egressWaiter struct {
	arrival uint64
	hash    comparison.Hash
	source  Option[comparison.Hash]
	// ready has one slot and receives at most one reference, so delivery never blocks.
	ready chan egressDelivery
}

func newEgressWaiter(arrival uint64, hash comparison.Hash, source Option[comparison.Hash]) *egressWaiter {
	return &egressWaiter{arrival: arrival, hash: hash, source: source, ready: make(chan egressDelivery, 1)}
}

func (w *egressWaiter) arrived() uint64 {
	return w.arrival
}

// hashes returns the hash the waiter claims a reference with, and any source hash it also accepts.
func (w *egressWaiter) hashes() (hash comparison.Hash, source Option[comparison.Hash]) {
	return w.hash, w.source
}

func (w *egressWaiter) deliver(delivery egressDelivery) {
	w.ready <- delivery
}

func (w *egressWaiter) delivered() <-chan egressDelivery {
	return w.ready
}

// errReferenceCancelled refuses a candidate whose matching reference request was
// cancelled. Its ingress request was most likely cancelled too, so neither is compared.
var errReferenceCancelled = errors.New("reference request was cancelled")

// egressOutcome is how a reference request finished.
type egressOutcome struct {
	raw     Option[Raw]
	failure Option[error]
	// cancelled means the reference gave up on the request, so its failure says
	// nothing about the dependency.
	cancelled bool
	// content is the decoded response of a source reference.
	content Option[Content]
}

// egressExchange is one forwarded reference request.
type egressExchange struct {
	arrival  uint64
	protocol model.Protocol
	route    model.Route
	// source reports that replacements read the response, so it is decoded when it finishes.
	source bool
	// optional reports that the candidate may leave the request out.
	optional bool
	outcome  *future.Future[egressOutcome]

	mu        sync.Mutex
	abandoned bool
	// matched reports that a replacing request used the source reference, which
	// pairs it without claiming it.
	matched bool
	// forgotten reports that replacing requests no longer find the source reference.
	forgotten bool
	expiry    Option[*time.Timer]
}

func newEgressExchange(arrival uint64, protocol model.Protocol, route model.Route, source, optional bool) *egressExchange {
	return &egressExchange{
		arrival: arrival, protocol: protocol, route: route, source: source, optional: optional,
		outcome: future.New[egressOutcome](),
	}
}

func (e *egressExchange) arrived() uint64 {
	return e.arrival
}

// isSource reports whether replacements read the response.
func (e *egressExchange) isSource() bool {
	return e.source
}

// markMatched records that a replacing request used the source reference.
func (e *egressExchange) markMatched() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.matched = true
}

func (e *egressExchange) isMatched() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.matched
}

// leftOut is why the candidate is quarantined when no request of its matched the reference.
func (e *egressExchange) leftOut(window time.Duration) model.QuarantineReason {
	return egressReason(model.QuarantineUnmatched, e.protocol, e.route, errors.Errorf("no matching candidate request within %s", window))
}

// leaveOut quarantines the candidate for leaving the request out, or logs it if the request is optional.
func (e *egressExchange) leaveOut(ctx context.Context, quarantine Quarantiner, window time.Duration) {
	if !e.optional {
		quarantine.Quarantine(ctx, e.leftOut(window))
		return
	}
	logger.FromContext(ctx).InfoContext(ctx, "Candidate left out an optional reference request",
		"event", logger.EventCorrelation, "protocol", e.protocol, "route", e.route)
}

// expireAfter calls expire once window passes, unless stopExpiry is called first.
func (e *egressExchange) expireAfter(window time.Duration, expire func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expiry = Some(time.AfterFunc(window, expire))
}

func (e *egressExchange) stopExpiry() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if timer, ok := e.expiry.Get(); ok {
		timer.Stop()
	}
}

// finish records the reference outcome, releasing it at once if nobody can claim it.
func (e *egressExchange) finish(outcome egressOutcome) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.outcome.Set(outcome)
	if e.abandoned {
		e.release()
	}
	if e.forgotten {
		e.releaseHold()
	}
}

// abandon marks the exchange as one no candidate will replay.
func (e *egressExchange) abandon() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.abandoned = true
	e.release()
}

// forget marks the source reference as one replacements no longer find.
func (e *egressExchange) forget() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.forgotten = true
	e.releaseHold()
}

// release frees the recorded response, if there is one yet. Callers hold e.mu.
func (e *egressExchange) release() {
	outcome, finished := e.outcome.Get()
	if !finished {
		return
	}
	if raw, ok := outcome.raw.Get(); ok {
		raw.Release()
	}
}

// releaseHold frees the memory held for the decoded source response, if there is
// one yet. Replacements already reading it keep it. Callers hold e.mu.
func (e *egressExchange) releaseHold() {
	outcome, finished := e.outcome.Get()
	if !finished {
		return
	}
	if content, ok := outcome.content.Get(); ok {
		if hold, held := content.Hold.Get(); held {
			hold.Release()
		}
	}
}

// wait blocks until the reference response is complete, then returns it for replay.
func (e *egressExchange) wait(ctx context.Context) (Raw, error) {
	outcome, err := e.outcome.Wait(ctx)
	if err != nil {
		e.abandon()
		return nil, errors.Wrap(err, "wait for reference response")
	}
	if failure, failed := outcome.failure.Get(); failed {
		if outcome.cancelled {
			return nil, errors.Errorf("%w: %w", errReferenceCancelled, failure)
		}
		return nil, errors.Wrap(failure, "reference response cannot be replayed")
	}
	raw, ok := outcome.raw.Get()
	if !ok {
		return nil, errors.New("reference response is missing")
	}
	return raw, nil
}

// decoded waits for a source reference response and returns its value. It
// outlives the recorded response, so it never claims or releases it.
func (e *egressExchange) decoded(ctx context.Context) (any, error) {
	outcome, err := e.outcome.Wait(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "wait for source reference response")
	}
	if failure, failed := outcome.failure.Get(); failed {
		if outcome.cancelled {
			return nil, errors.Errorf("source %w: %w", errReferenceCancelled, failure)
		}
		return nil, errors.Wrap(failure, "source reference response failed")
	}
	content, ok := outcome.content.Get()
	if !ok {
		return nil, errors.New("source reference response was not decoded")
	}
	if failure, failed := content.Failure.Get(); failed {
		return nil, errors.Wrap(failure, "decode source reference response")
	}
	message, ok := content.Message.Get()
	if !ok {
		return nil, errors.New("source reference response has no message")
	}
	return message.Value, nil
}

// egressReference forwards every dependency request at once and hashes it in
// the background, so failing to identify a request never affects the reference.
type egressReference struct {
	queue    *egressQueue
	protocol model.Protocol
}

func newEgressReference(queue *egressQueue, protocol model.Protocol) *egressReference {
	return &egressReference{queue: queue, protocol: protocol}
}

func (r *egressReference) ProcessRequest(ctx context.Context, _ Option[RequestID], route model.Route, _ Option[string],
	request func(ctx context.Context) (Message, error),
) (Decision, error) {
	return r.queue.forward(ctx, r.protocol, route, request), nil
}

func (r *egressReference) ProcessResponse(ctx context.Context, exchange Exchange, response Response) {
	pending, ok := r.queue.take(ctx, exchange)
	if !ok {
		response.Raw.Release()
		return
	}
	content := None[Content]()
	if pending.isSource() {
		content = Some(response.Decode(ctx))
	}
	pending.finish(egressOutcome{raw: Some(response.Raw), content: content})
}

// Unable with a cancelled context means the reference gave up on the request,
// not that the dependency failed.
func (r *egressReference) Unable(ctx context.Context, exchange Exchange, reason error) {
	if pending, ok := r.queue.take(ctx, exchange); ok {
		pending.finish(egressOutcome{failure: Some(reason), cancelled: ctx.Err() != nil})
	}
}

// egressCandidate answers each dependency request with an equivalent reference
// response. A request that cannot be matched quarantines the candidate.
type egressCandidate struct {
	queue    *egressQueue
	protocol model.Protocol
}

func newEgressCandidate(queue *egressQueue, protocol model.Protocol) *egressCandidate {
	return &egressCandidate{queue: queue, protocol: protocol}
}

func (c *egressCandidate) ProcessRequest(ctx context.Context, _ Option[RequestID], route model.Route, _ Option[string],
	request func(ctx context.Context) (Message, error),
) (Decision, error) {
	return c.queue.intercept(ctx, c.protocol, route, request)
}

// ProcessResponse is a server error: candidate requests are never forwarded.
func (c *egressCandidate) ProcessResponse(ctx context.Context, exchange Exchange, response Response) {
	response.Raw.Release()
	c.queue.misreported(ctx, exchange, None[error]())
}

func (c *egressCandidate) Unable(ctx context.Context, exchange Exchange, reason error) {
	c.queue.misreported(ctx, exchange, Some(reason))
}

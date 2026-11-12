package protocol

import (
	"cmp"
	"context"
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
	// derivations answers candidate requests to an endpoint by derivation alone,
	// and sourceResponses types the reference responses derivations read.
	derivations     map[endpoint]javascript.Derivation
	sourceResponses map[endpoint]schema.TypeRef
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
	// sources keeps source references for the window whether or not a candidate
	// claims them, and observers wait for one; neither claims a reference.
	sources   map[comparison.Hash][]*egressExchange
	observers map[comparison.Hash][]chan *egressExchange
}

func newEgressQueue(scripts *comparison.Scripts, window time.Duration, quarantine Quarantiner) *egressQueue {
	derivations := map[endpoint]javascript.Derivation{}
	sourceResponses := map[endpoint]schema.TypeRef{}
	for _, derivation := range scripts.Derivations() {
		derivations[endpoint{derivation.Protocol(), derivation.Pattern()}] = derivation
		sourceResponses[endpoint{derivation.Protocol(), derivation.Source()}] = derivation.SourceResponse()
	}
	return &egressQueue{
		scripts:         scripts,
		window:          window,
		quarantine:      quarantine,
		derivations:     derivations,
		sourceResponses: sourceResponses,
		exchanges:       map[Exchange]*egressExchange{},
		unused:          map[comparison.Hash][]*egressExchange{},
		waiting:         map[comparison.Hash][]*egressWaiter{},
		sources:         map[comparison.Hash][]*egressExchange{},
		observers:       map[comparison.Hash][]chan *egressExchange{},
		idle:            closedChannel(),
	}
}

// sourceResponse returns the type a derivation reads an endpoint's reference responses as, if any does.
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
	_, source := q.sourceResponses[endpoint{protocol, string(route)}]
	pending := newEgressExchange(q.arrivals, source)
	q.exchanges[exchange] = pending
	q.startHashing()
	q.mu.Unlock()
	background := context.WithoutCancel(ctx)
	go func() {
		defer q.finishHashing()
		hash, err := q.hash(background, protocol, route, "reference", request)
		if err != nil {
			pending.abandon()
			q.quarantine.Quarantine(background, egressReason(model.QuarantineUnable, protocol, route, errors.Wrap(err, "identify reference request")))
			return
		}
		q.add(hash, pending)
	}()
	return DecisionForward{RequestID: None[RequestID](), Exchange: exchange}
}

// intercept answers a candidate request with an equivalent reference response.
func (q *egressQueue) intercept(ctx context.Context, protocol model.Protocol, route model.Route,
	request func(ctx context.Context) (Message, error),
) (Decision, error) {
	if derivation, derived := q.derivations[endpoint{protocol, string(route)}]; derived {
		return q.answer(ctx, protocol, route, derivation, request)
	}
	raw, kind, err := q.match(ctx, protocol, route, request)
	if err != nil {
		if blamesCandidate(ctx, kind) {
			q.quarantine.Quarantine(ctx, egressReason(kind, protocol, route, err))
		}
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
	waiter := newEgressWaiter(q.arrive())
	hash, err := q.hash(ctx, protocol, route, "candidate", request)
	if err != nil {
		return nil, model.QuarantineUnable, errors.Wrap(err, "identify candidate request")
	}
	pending, err := q.claim(ctx, hash, waiter)
	if err != nil {
		return nil, model.QuarantineUnmatched, err
	}
	raw, err := pending.wait(ctx)
	if err != nil {
		return nil, model.QuarantineUnable, err
	}
	return raw, "", nil
}

// blamesCandidate reports whether a failed match quarantines the candidate. Giving up
// explains a failure, except giving up unmatched; work the proxy stopped never does.
func blamesCandidate(ctx context.Context, kind model.QuarantineKind) bool {
	if ctx.Err() == nil {
		return true
	}
	return kind == model.QuarantineUnmatched && !errors.Is(context.Cause(ctx), ErrCandidateStopped)
}

// answer answers a candidate request with a response derived from a source reference response.
func (q *egressQueue) answer(ctx context.Context, protocol model.Protocol, route model.Route, derivation javascript.Derivation,
	request func(ctx context.Context) (Message, error),
) (Decision, error) {
	message, kind, err := q.derive(ctx, protocol, derivation, request)
	if err != nil {
		// The candidate giving up explains the failure.
		if ctx.Err() == nil {
			q.quarantine.Quarantine(ctx, egressReason(kind, protocol, route, err))
		}
		return nil, err
	}
	logger.FromContext(ctx).InfoContext(ctx, "Candidate request was answered from a reference response",
		"event", logger.EventCorrelation, "protocol", protocol, "route", route, "source", derivation.Source())
	return DecisionAnswer{Message: message}, nil
}

// derive returns the answer to a candidate request, or why there is none.
func (q *egressQueue) derive(ctx context.Context, protocol model.Protocol, derivation javascript.Derivation,
	request func(ctx context.Context) (Message, error),
) (Message, model.QuarantineKind, error) {
	message, err := request(ctx)
	if err != nil {
		return Message{}, model.QuarantineUnable, errors.Wrap(err, "identify candidate request")
	}
	sourceRequest, err := q.scripts.DeriveRequest(ctx, derivation, message.Value)
	if err != nil {
		return Message{}, model.QuarantineUnable, errors.Wrap(err, "map candidate request to a source request")
	}
	identity := string(protocol) + " " + derivation.Source()
	hash, err := q.scripts.Hash(ctx, identity, Some(derivation.SourceRequest()), "candidate", sourceRequest)
	if err != nil {
		return Message{}, model.QuarantineUnable, errors.Wrap(err, "identify source request")
	}
	pending, err := q.observe(ctx, hash)
	if err != nil {
		return Message{}, model.QuarantineUnmatched, err
	}
	reference, err := pending.decoded(ctx)
	if err != nil {
		return Message{}, model.QuarantineUnable, err
	}
	answer, err := q.scripts.DeriveResponse(ctx, derivation, message.Value, reference)
	if err != nil {
		return Message{}, model.QuarantineUnable, errors.Wrap(err, "derive response")
	}
	return Message{Type: Some(derivation.Response()), Value: answer}, "", nil
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
// for the window before releasing it.
func (q *egressQueue) add(hash comparison.Hash, pending *egressExchange) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if pending.isSource() {
		// Every observer receives a source reference, and later ones find it for the window.
		for _, observer := range q.observers[hash] {
			observer <- pending
		}
		delete(q.observers, hash)
		q.sources[hash] = insertByArrival(q.sources[hash], pending)
		time.AfterFunc(q.window, func() { q.forget(hash, pending) })
	}
	if waiters := q.waiting[hash]; len(waiters) > 0 {
		q.setWaiting(hash, waiters[1:])
		waiters[0].deliver(pending)
		return
	}
	q.unused[hash] = insertByArrival(q.unused[hash], pending)
	pending.expireAfter(q.window, func() { q.expire(hash, pending) })
}

// forget stops offering a source reference to observers.
func (q *egressQueue) forget(hash comparison.Hash, pending *egressExchange) {
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

// observe returns the earliest source reference for hash, waiting up to the
// window for one to arrive. It never claims the reference.
func (q *egressQueue) observe(ctx context.Context, hash comparison.Hash) (*egressExchange, error) {
	q.mu.Lock()
	if sources := q.sources[hash]; len(sources) > 0 {
		q.mu.Unlock()
		return sources[0], nil
	}
	// observer has one slot and receives at most one reference, so add never blocks.
	observer := make(chan *egressExchange, 1)
	q.observers[hash] = append(q.observers[hash], observer)
	q.mu.Unlock()

	timer := time.NewTimer(q.window)
	defer timer.Stop()
	var err error
	select {
	case pending := <-observer:
		return pending, nil
	case <-timer.C:
		err = errors.Errorf("no matching source reference request within %s", q.window)
	case <-ctx.Done():
		err = errors.Wrap(ctx.Err(), "wait for matching source reference request")
	}
	q.mu.Lock()
	observers := q.observers[hash]
	index := slices.Index(observers, observer)
	if index >= 0 {
		observers = slices.Delete(observers, index, index+1)
		if len(observers) == 0 {
			delete(q.observers, hash)
		} else {
			q.observers[hash] = observers
		}
	}
	q.mu.Unlock()
	if index < 0 {
		// add removed this observer concurrently, so a reference is already on its way.
		return <-observer, nil
	}
	return nil, err
}

// claim takes the earliest unused reference for hash, waiting up to the window
// for one to arrive. Ordering covers only hashed references, so it never waits on one still hashing.
func (q *egressQueue) claim(ctx context.Context, hash comparison.Hash, waiter *egressWaiter) (*egressExchange, error) {
	q.mu.Lock()
	if unused := q.unused[hash]; len(unused) > 0 {
		q.setUnused(hash, unused[1:])
		unused[0].stopExpiry()
		q.mu.Unlock()
		return unused[0], nil
	}
	q.waiting[hash] = insertByArrival(q.waiting[hash], waiter)
	q.mu.Unlock()

	started := time.Now()
	timer := time.NewTimer(q.window)
	defer timer.Stop()
	var err error
	select {
	case pending := <-waiter.delivered():
		return pending, nil
	case <-timer.C:
		err = errors.Errorf("no matching reference request within %s", q.window)
	case <-ctx.Done():
		err = errors.Wrapf(ctx.Err(), "no matching reference request before the candidate gave up after %s",
			time.Since(started).Round(time.Millisecond))
	}
	q.mu.Lock()
	waiters := q.waiting[hash]
	index := slices.Index(waiters, waiter)
	if index >= 0 {
		q.setWaiting(hash, slices.Delete(waiters, index, index+1))
	}
	q.mu.Unlock()
	if index < 0 {
		// add removed this waiter concurrently, so a reference is already on its way.
		return <-waiter.delivered(), nil
	}
	return nil, err
}

// expire releases a reference that no candidate claimed within the window.
func (q *egressQueue) expire(hash comparison.Hash, pending *egressExchange) {
	q.mu.Lock()
	unused := q.unused[hash]
	index := slices.Index(unused, pending)
	if index >= 0 {
		q.setUnused(hash, slices.Delete(unused, index, index+1))
	}
	q.mu.Unlock()
	if index >= 0 {
		pending.abandon()
	}
}

// setUnused replaces the unused queue for hash. Callers hold q.mu.
func (q *egressQueue) setUnused(hash comparison.Hash, unused []*egressExchange) {
	if len(unused) == 0 {
		delete(q.unused, hash)
		return
	}
	q.unused[hash] = unused
}

// setWaiting replaces the waiting queue for hash. Callers hold q.mu.
func (q *egressQueue) setWaiting(hash comparison.Hash, waiters []*egressWaiter) {
	if len(waiters) == 0 {
		delete(q.waiting, hash)
		return
	}
	q.waiting[hash] = waiters
}

// insertByArrival keeps a queue in arrival order.
func insertByArrival[T interface{ arrived() uint64 }](queue []T, item T) []T {
	index, _ := slices.BinarySearchFunc(queue, item.arrived(), func(queued T, arrival uint64) int {
		return cmp.Compare(queued.arrived(), arrival)
	})
	return slices.Insert(queue, index, item)
}

// egressWaiter is a candidate waiting for a matching reference.
type egressWaiter struct {
	arrival uint64
	// ready has one slot and receives at most one reference, so delivery never blocks.
	ready chan *egressExchange
}

func newEgressWaiter(arrival uint64) *egressWaiter {
	return &egressWaiter{arrival: arrival, ready: make(chan *egressExchange, 1)}
}

func (w *egressWaiter) arrived() uint64 {
	return w.arrival
}

func (w *egressWaiter) deliver(pending *egressExchange) {
	w.ready <- pending
}

func (w *egressWaiter) delivered() <-chan *egressExchange {
	return w.ready
}

// egressOutcome is how a reference request finished.
type egressOutcome struct {
	raw     Option[Raw]
	failure Option[error]
	// content is the decoded response of a source reference.
	content Option[Content]
}

// egressExchange is one forwarded reference request.
type egressExchange struct {
	arrival uint64
	// source reports that derivations read the response, so it is decoded when it finishes.
	source  bool
	outcome *future.Future[egressOutcome]

	mu        sync.Mutex
	abandoned bool
	expiry    Option[*time.Timer]
}

func newEgressExchange(arrival uint64, source bool) *egressExchange {
	return &egressExchange{arrival: arrival, source: source, outcome: future.New[egressOutcome]()}
}

func (e *egressExchange) arrived() uint64 {
	return e.arrival
}

// isSource reports whether derivations read the response.
func (e *egressExchange) isSource() bool {
	return e.source
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
func (e *egressExchange) finish(raw Option[Raw], failure Option[error], content Option[Content]) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.outcome.Set(egressOutcome{raw: raw, failure: failure, content: content})
	if e.abandoned {
		e.release()
	}
}

// abandon marks the exchange as one no candidate will replay.
func (e *egressExchange) abandon() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.abandoned = true
	e.release()
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

// wait blocks until the reference response is complete, then returns it for replay.
func (e *egressExchange) wait(ctx context.Context) (Raw, error) {
	outcome, err := e.outcome.Wait(ctx)
	if err != nil {
		e.abandon()
		return nil, errors.Wrap(err, "wait for reference response")
	}
	if failure, failed := outcome.failure.Get(); failed {
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
	pending.finish(Some(response.Raw), None[error](), content)
}

func (r *egressReference) Unable(ctx context.Context, exchange Exchange, reason error) {
	if pending, ok := r.queue.take(ctx, exchange); ok {
		pending.finish(None[Raw](), Some(reason), None[Content]())
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

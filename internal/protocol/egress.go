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
	"github.com/block/spectre/internal/logger"
)

// egressQueue pairs candidate and reference requests of the same hash in arrival
// order. Each reference is claimed once, so duplicates pair one-to-one.
type egressQueue struct {
	scripts    *comparison.Scripts
	window     time.Duration
	quarantine Quarantiner
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
}

func newEgressQueue(scripts *comparison.Scripts, window time.Duration, quarantine Quarantiner) *egressQueue {
	return &egressQueue{
		scripts:    scripts,
		window:     window,
		quarantine: quarantine,
		exchanges:  map[Exchange]*egressExchange{},
		unused:     map[comparison.Hash][]*egressExchange{},
		waiting:    map[comparison.Hash][]*egressWaiter{},
		idle:       closedChannel(),
	}
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
func (q *egressQueue) hash(ctx context.Context, protocol javascript.Protocol, route Route, side string,
	request func(ctx context.Context) (Message, error),
) (comparison.Hash, error) {
	message, err := request(ctx)
	if err != nil {
		return comparison.Hash{}, errors.WithStack(err)
	}
	return errors.WithStack2(q.scripts.Hash(ctx, string(protocol)+" "+string(route), message.Type, side, message.Value))
}

// forward registers a reference request and hashes it in the background.
func (q *egressQueue) forward(ctx context.Context, protocol javascript.Protocol, route Route,
	request func(ctx context.Context) (Message, error),
) Decision {
	exchange := newExchange()
	q.mu.Lock()
	q.arrivals++
	pending := newEgressExchange(q.arrivals)
	q.exchanges[exchange] = pending
	q.startHashing()
	q.mu.Unlock()
	background := context.WithoutCancel(ctx)
	go func() {
		defer q.finishHashing()
		hash, err := q.hash(background, protocol, route, "reference", request)
		if err != nil {
			pending.abandon()
			q.quarantine.Quarantine(background, errors.Wrap(err, "identify reference request"))
			return
		}
		q.add(hash, pending)
	}()
	return DecisionForward{RequestID: None[RequestID](), Exchange: exchange}
}

// intercept answers a candidate request with an equivalent reference response.
func (q *egressQueue) intercept(ctx context.Context, protocol javascript.Protocol, route Route,
	request func(ctx context.Context) (Message, error),
) (Decision, error) {
	raw, err := q.match(ctx, protocol, route, request)
	if err != nil {
		// The candidate giving up explains the failure.
		if ctx.Err() == nil {
			q.quarantine.Quarantine(ctx, err)
		}
		return nil, err
	}
	logger.FromContext(ctx).InfoContext(ctx, "Candidate request matched a reference request",
		"event", logger.EventCorrelation, "protocol", protocol, "route", route)
	return DecisionIntercept{Raw: raw}, nil
}

func (q *egressQueue) match(ctx context.Context, protocol javascript.Protocol, route Route,
	request func(ctx context.Context) (Message, error),
) (Raw, error) {
	waiter := newEgressWaiter(q.arrive())
	hash, err := q.hash(ctx, protocol, route, "candidate", request)
	if err != nil {
		return nil, errors.Wrap(err, "identify candidate request")
	}
	pending, err := q.claim(ctx, hash, waiter)
	if err != nil {
		return nil, err
	}
	return errors.WithStack2(pending.wait(ctx))
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
	if waiters := q.waiting[hash]; len(waiters) > 0 {
		q.setWaiting(hash, waiters[1:])
		waiters[0].deliver(pending)
		return
	}
	q.unused[hash] = insertByArrival(q.unused[hash], pending)
	pending.expireAfter(q.window, func() { q.expire(hash, pending) })
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

	timer := time.NewTimer(q.window)
	defer timer.Stop()
	var err error
	select {
	case pending := <-waiter.delivered():
		return pending, nil
	case <-timer.C:
		err = errors.Errorf("no matching reference request within %s", q.window)
	case <-ctx.Done():
		err = errors.Wrap(ctx.Err(), "wait for matching reference request")
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

// egressExchange is one forwarded reference request. Its outcome is written once
// before done closes, and read only after it closes.
type egressExchange struct {
	arrival uint64
	done    chan struct{}
	raw     Option[Raw]
	failure Option[error]

	mu        sync.Mutex
	finished  bool
	abandoned bool
	expiry    Option[*time.Timer]
}

func newEgressExchange(arrival uint64) *egressExchange {
	return &egressExchange{arrival: arrival, done: make(chan struct{})}
}

func (e *egressExchange) arrived() uint64 {
	return e.arrival
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
func (e *egressExchange) finish(raw Option[Raw], failure Option[error]) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.raw, e.failure = raw, failure
	e.finished = true
	close(e.done)
	if e.abandoned {
		e.release()
	}
}

// abandon marks the exchange as one no candidate will replay.
func (e *egressExchange) abandon() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.abandoned = true
	if e.finished {
		e.release()
	}
}

// release frees the recorded response. Callers hold e.mu.
func (e *egressExchange) release() {
	if raw, ok := e.raw.Get(); ok {
		raw.Release()
	}
}

// wait blocks until the reference response is complete, then returns it for replay.
func (e *egressExchange) wait(ctx context.Context) (Raw, error) {
	select {
	case <-e.done:
	case <-ctx.Done():
		e.abandon()
		return nil, errors.Wrap(ctx.Err(), "wait for reference response")
	}
	if failure, failed := e.failure.Get(); failed {
		return nil, errors.Wrap(failure, "reference response cannot be replayed")
	}
	raw, ok := e.raw.Get()
	if !ok {
		return nil, errors.New("reference response is missing")
	}
	return raw, nil
}

// egressReference forwards every dependency request at once and hashes it in
// the background, so failing to identify a request never affects the reference.
type egressReference struct {
	queue    *egressQueue
	protocol javascript.Protocol
}

func newEgressReference(queue *egressQueue, protocol javascript.Protocol) *egressReference {
	return &egressReference{queue: queue, protocol: protocol}
}

func (r *egressReference) ProcessRequest(ctx context.Context, _ Option[RequestID], route Route, _ Option[string],
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
	pending.finish(Some(response.Raw), None[error]())
}

func (r *egressReference) Unable(ctx context.Context, exchange Exchange, reason error) {
	if pending, ok := r.queue.take(ctx, exchange); ok {
		pending.finish(None[Raw](), Some(reason))
	}
}

// egressCandidate answers each dependency request with an equivalent reference
// response. A request that cannot be matched quarantines the candidate.
type egressCandidate struct {
	queue    *egressQueue
	protocol javascript.Protocol
}

func newEgressCandidate(queue *egressQueue, protocol javascript.Protocol) *egressCandidate {
	return &egressCandidate{queue: queue, protocol: protocol}
}

func (c *egressCandidate) ProcessRequest(ctx context.Context, _ Option[RequestID], route Route, _ Option[string],
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

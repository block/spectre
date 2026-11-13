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
)

// egressRoute names the requests of one protocol to one route.
type egressRoute struct {
	protocol model.Protocol
	route    model.Route
}

// egressQueue pairs candidate and reference requests of the same hash in arrival
// order. Each reference is claimed once, so duplicates pair one-to-one.
type egressQueue struct {
	scripts    *comparison.Scripts
	window     time.Duration
	quarantine Quarantiner
	// derivations are keyed by derived route, and sources holds the routes they derive from.
	derivations map[egressRoute]javascript.Derivation
	sources     map[egressRoute]bool
	// mu guards every map. A hash never has unused references and waiting
	// candidates at the same time, because add hands over to a waiter first.
	mu sync.Mutex
	// arrivals numbers requests as they arrive, because hashing may finish out of order.
	arrivals uint64
	// background counts reference hashing and source decoding, which may finish after
	// their request. idle is closed whenever it is zero, so shutdown can wait as work starts.
	background int
	idle       chan struct{}
	exchanges  map[Exchange]*egressExchange
	unused     map[comparison.Hash][]*egressExchange
	waiting    map[comparison.Hash][]*egressWaiter
	// offered holds source references for derivations, which never claim them, so
	// each stays offered for the window. deriving holds candidates by source hash.
	offered  map[comparison.Hash][]*egressExchange
	deriving map[comparison.Hash][]*egressWaiter
}

func newEgressQueue(scripts *comparison.Scripts, window time.Duration, quarantine Quarantiner) *egressQueue {
	derivations := map[egressRoute]javascript.Derivation{}
	sources := map[egressRoute]bool{}
	for _, derivation := range scripts.Derivations() {
		protocol := derivation.Endpoint().Protocol()
		derivations[egressRoute{protocol: protocol, route: derivation.Endpoint().Route()}] = derivation
		sources[egressRoute{protocol: protocol, route: derivation.Source()}] = true
	}
	return &egressQueue{
		scripts:     scripts,
		window:      window,
		quarantine:  quarantine,
		derivations: derivations,
		sources:     sources,
		exchanges:   map[Exchange]*egressExchange{},
		unused:      map[comparison.Hash][]*egressExchange{},
		waiting:     map[comparison.Hash][]*egressWaiter{},
		offered:     map[comparison.Hash][]*egressExchange{},
		deriving:    map[comparison.Hash][]*egressWaiter{},
		idle:        closedChannel(),
	}
}

func closedChannel() chan struct{} {
	idle := make(chan struct{})
	close(idle)
	return idle
}

// startBackground records background work in progress. Callers hold q.mu.
func (q *egressQueue) startBackground() {
	if q.background == 0 {
		q.idle = make(chan struct{})
	}
	q.background++
}

func (q *egressQueue) finishBackground() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.background--
	if q.background == 0 {
		close(q.idle)
	}
}

func (q *egressQueue) arrive() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.arrivals++
	return q.arrivals
}

// shutdown waits until no reference is being hashed or decoded.
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
// Normalising changes the payload's value.
func (q *egressQueue) hash(ctx context.Context, protocol model.Protocol, route model.Route, side string, message Message) (comparison.Hash, error) {
	return errors.WithStack2(q.scripts.Hash(ctx, string(protocol)+" "+string(route), message.Type, side, message.Value))
}

// forward registers a reference request and hashes it in the background.
func (q *egressQueue) forward(ctx context.Context, protocol model.Protocol, route model.Route,
	request func(ctx context.Context) (Message, error),
) Decision {
	exchange := newExchange()
	q.mu.Lock()
	q.arrivals++
	pending := newEgressExchange(q.arrivals, q.sources[egressRoute{protocol: protocol, route: route}])
	q.exchanges[exchange] = pending
	q.startBackground()
	q.mu.Unlock()
	background := context.WithoutCancel(ctx)
	go func() {
		defer q.finishBackground()
		hash, err := q.identify(background, protocol, route, request)
		if err != nil {
			pending.abandon()
			pending.withdraw()
			q.quarantine.Quarantine(background, egressReason(model.QuarantineUnable, protocol, route, errors.Wrap(err, "identify reference request")))
			return
		}
		q.add(hash, pending)
	}()
	return DecisionForward{RequestID: None[RequestID](), Exchange: exchange}
}

func (q *egressQueue) identify(ctx context.Context, protocol model.Protocol, route model.Route,
	request func(ctx context.Context) (Message, error),
) (comparison.Hash, error) {
	message, err := request(ctx)
	if err != nil {
		return comparison.Hash{}, errors.WithStack(err)
	}
	return q.hash(ctx, protocol, route, "reference", message)
}

// finish records a reference response. A source's response is decoded in the
// background first, so the decoded message outlives its replay.
func (q *egressQueue) finish(ctx context.Context, pending *egressExchange, response Response) {
	if !pending.isSource() {
		pending.finish(Some(response.Raw), None[error](), None[Content]())
		return
	}
	q.mu.Lock()
	q.startBackground()
	q.mu.Unlock()
	background := context.WithoutCancel(ctx)
	go func() {
		defer q.finishBackground()
		pending.finish(Some(response.Raw), None[error](), Some(response.Decode(background)))
	}()
}

// intercept answers a candidate request with an equivalent reference response,
// or with one derived from a reference response to its source.
func (q *egressQueue) intercept(ctx context.Context, protocol model.Protocol, route model.Route,
	request func(ctx context.Context) (Message, error),
) (Decision, error) {
	raw, source, kind, err := q.match(ctx, protocol, route, request)
	if err != nil {
		if blamesCandidate(ctx, kind) {
			q.quarantine.Quarantine(ctx, egressReason(kind, protocol, route, err))
		}
		return nil, err
	}
	log := logger.FromContext(ctx)
	if derived, ok := source.Get(); ok {
		log.InfoContext(ctx, "Candidate request was derived from a reference request",
			"event", logger.EventCorrelation, "protocol", protocol, "route", route, "source", derived)
	} else {
		log.InfoContext(ctx, "Candidate request matched a reference request",
			"event", logger.EventCorrelation, "protocol", protocol, "route", route)
	}
	return DecisionIntercept{Raw: raw}, nil
}

// match returns the response to replay and the route it was derived from, if any, or why there is none.
func (q *egressQueue) match(ctx context.Context, protocol model.Protocol, route model.Route,
	request func(ctx context.Context) (Message, error),
) (Raw, Option[model.Route], model.QuarantineKind, error) {
	unmatched := None[model.Route]()
	waiter := newEgressWaiter(q.arrive())
	message, err := request(ctx)
	if err != nil {
		return nil, unmatched, model.QuarantineUnable, errors.Wrap(err, "identify candidate request")
	}
	derivation, derived := q.derivations[egressRoute{protocol: protocol, route: route}]
	original := message.Value
	if derived {
		message.Value = detached(original)
	}
	hash, err := q.hash(ctx, protocol, route, "candidate", message)
	if err != nil {
		return nil, unmatched, model.QuarantineUnable, errors.Wrap(err, "identify candidate request")
	}
	source := None[comparison.Hash]()
	if derived {
		sourceHash, err := q.sourceHash(ctx, protocol, derivation, original)
		if err != nil {
			return nil, unmatched, model.QuarantineUnable, err
		}
		source = Some(sourceHash)
	}
	choice, err := q.choose(ctx, hash, source, waiter)
	if err != nil {
		return nil, unmatched, model.QuarantineUnmatched, err
	}
	if !choice.derived {
		raw, err := choice.pending.wait(ctx)
		if err != nil {
			return nil, unmatched, model.QuarantineUnable, err
		}
		return raw, unmatched, "", nil
	}
	raw, err := q.answer(ctx, derivation, original, message.Encode, choice.pending)
	if err != nil {
		return nil, unmatched, model.QuarantineUnable, err
	}
	return raw, Some(derivation.Source()), "", nil
}

// sourceHash maps a candidate request to its source and identifies it as a request to the source.
func (q *egressQueue) sourceHash(ctx context.Context, protocol model.Protocol, derivation javascript.Derivation, request any) (comparison.Hash, error) {
	mapped, err := q.scripts.DeriveRequest(ctx, derivation, request)
	if err != nil {
		return comparison.Hash{}, errors.Wrap(err, "map candidate request to its source")
	}
	message := Message{Type: Some(derivation.SourceRequest()), Value: mapped}
	hash, err := q.hash(ctx, protocol, derivation.Source(), "candidate", message)
	return hash, errors.Wrap(err, "identify candidate request to its source")
}

// answer derives a response from a source reference's decoded response and encodes it for the candidate.
func (q *egressQueue) answer(ctx context.Context, derivation javascript.Derivation, request any,
	encode Option[Encoder], pending *egressExchange,
) (Raw, error) {
	defer pending.unhold()
	content, err := pending.decoded(ctx)
	if err != nil {
		return nil, err
	}
	if status, known := content.Status.Get(); known && status != "200" && status != "grpc-status 0" {
		return nil, errors.Errorf("source reference response has status %q", status)
	}
	if failure, failed := content.Failure.Get(); failed {
		return nil, errors.Wrap(failure, "source reference response cannot be decoded")
	}
	reference, ok := content.Message.Get()
	if !ok {
		return nil, errors.New("source reference response has no message")
	}
	value, err := q.scripts.DeriveResponse(ctx, derivation, request, reference.Value)
	if err != nil {
		return nil, errors.Wrap(err, "derive response")
	}
	encoder, ok := encode.Get()
	if !ok {
		return nil, errors.New("candidate request cannot be answered with a derived response")
	}
	raw, err := encoder(ctx, Message{Type: Some(derivation.Response()), Value: value})
	return raw, errors.Wrap(err, "encode derived response")
}

// detached copies a decoded value, so normalising the copy leaves the value intact.
func detached(value any) any {
	switch value := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(value))
		for key, item := range value {
			copied[key] = detached(item)
		}
		return copied
	case []any:
		copied := make([]any, len(value))
		for index, item := range value {
			copied[index] = detached(item)
		}
		return copied
	default:
		return value
	}
}

// blamesCandidate reports whether a failed match quarantines the candidate. Giving up
// explains a failure, except giving up unmatched; work the proxy stopped never does.
func blamesCandidate(ctx context.Context, kind model.QuarantineKind) bool {
	if ctx.Err() == nil {
		return true
	}
	return kind == model.QuarantineUnmatched && !errors.Is(context.Cause(ctx), ErrCandidateStopped)
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
// for the window before releasing it. A source is also offered to every derivation.
func (q *egressQueue) add(hash comparison.Hash, pending *egressExchange) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if waiters := q.waiting[hash]; len(waiters) > 0 {
		q.deliver(waiters[0], egressChoice{pending: pending})
	} else {
		q.unused[hash] = insertByArrival(q.unused[hash], pending)
		pending.expireAfter(q.window, func() { q.expire(hash, pending) })
	}
	if !pending.isSource() {
		return
	}
	q.offered[hash] = insertByArrival(q.offered[hash], pending)
	time.AfterFunc(q.window, func() { q.withdraw(hash, pending) })
	for _, waiter := range slices.Clone(q.deriving[hash]) {
		pending.hold()
		q.deliver(waiter, egressChoice{pending: pending, derived: true})
	}
}

// choose selects one reference for a candidate: the earliest unused one with its
// own hash, else the earliest offered source, else whichever arrives first within the window.
func (q *egressQueue) choose(ctx context.Context, hash comparison.Hash, source Option[comparison.Hash], waiter *egressWaiter) (egressChoice, error) {
	q.mu.Lock()
	// Queues are edited in place, so read their entries only under q.mu.
	if unused := q.unused[hash]; len(unused) > 0 {
		claimed := unused[0]
		q.setUnused(hash, unused[1:])
		claimed.stopExpiry()
		q.mu.Unlock()
		return egressChoice{pending: claimed}, nil
	}
	if sourceHash, derived := source.Get(); derived {
		if offered := q.offered[sourceHash]; len(offered) > 0 {
			observed := offered[0]
			observed.hold()
			q.mu.Unlock()
			return egressChoice{pending: observed, derived: true}, nil
		}
		q.deriving[sourceHash] = insertByArrival(q.deriving[sourceHash], waiter)
	}
	waiter.waitIn(hash, source)
	q.waiting[hash] = insertByArrival(q.waiting[hash], waiter)
	q.mu.Unlock()

	started := time.Now()
	timer := time.NewTimer(q.window)
	defer timer.Stop()
	var err error
	select {
	case choice := <-waiter.delivered():
		return choice, nil
	case <-timer.C:
		err = errors.Errorf("no matching reference request within %s", q.window)
	case <-ctx.Done():
		err = errors.Wrapf(ctx.Err(), "no matching reference request before the candidate gave up after %s",
			time.Since(started).Round(time.Millisecond))
	}
	q.mu.Lock()
	chosen := waiter.isChosen()
	if !chosen {
		q.forget(waiter)
	}
	q.mu.Unlock()
	if chosen {
		// add chose a reference concurrently, so it is already on its way.
		return <-waiter.delivered(), nil
	}
	return egressChoice{}, err
}

// deliver hands a waiter its one reference and stops it waiting for any other. Callers hold q.mu.
func (q *egressQueue) deliver(waiter *egressWaiter, choice egressChoice) {
	q.forget(waiter)
	waiter.deliver(choice)
}

// forget removes a waiter from every queue it waits in. Callers hold q.mu.
func (q *egressQueue) forget(waiter *egressWaiter) {
	hash, sourceHash := waiter.queues()
	q.setWaiting(hash, without(q.waiting[hash], waiter))
	if source, derived := sourceHash.Get(); derived {
		q.setDeriving(source, without(q.deriving[source], waiter))
	}
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

// withdraw stops offering a source to derivations once the window passes.
func (q *egressQueue) withdraw(hash comparison.Hash, pending *egressExchange) {
	q.mu.Lock()
	q.setOffered(hash, without(q.offered[hash], pending))
	q.mu.Unlock()
	pending.withdraw()
}

// setUnused replaces the unused queue for hash. Callers hold q.mu.
func (q *egressQueue) setUnused(hash comparison.Hash, unused []*egressExchange) {
	setQueue(q.unused, hash, unused)
}

// setWaiting replaces the waiting queue for hash. Callers hold q.mu.
func (q *egressQueue) setWaiting(hash comparison.Hash, waiters []*egressWaiter) {
	setQueue(q.waiting, hash, waiters)
}

// setOffered replaces the offered sources for hash. Callers hold q.mu.
func (q *egressQueue) setOffered(hash comparison.Hash, offered []*egressExchange) {
	setQueue(q.offered, hash, offered)
}

// setDeriving replaces the derivations waiting for a source with hash. Callers hold q.mu.
func (q *egressQueue) setDeriving(hash comparison.Hash, waiters []*egressWaiter) {
	setQueue(q.deriving, hash, waiters)
}

// setQueue replaces one hash's queue, dropping it once empty.
func setQueue[T any](queues map[comparison.Hash][]T, hash comparison.Hash, queue []T) {
	if len(queue) == 0 {
		delete(queues, hash)
		return
	}
	queues[hash] = queue
}

// without returns queue without item, which it may modify.
func without[T comparable](queue []T, item T) []T {
	index := slices.Index(queue, item)
	if index < 0 {
		return queue
	}
	return slices.Delete(queue, index, index+1)
}

// insertByArrival keeps a queue in arrival order.
func insertByArrival[T interface{ arrived() uint64 }](queue []T, item T) []T {
	index, _ := slices.BinarySearchFunc(queue, item.arrived(), func(queued T, arrival uint64) int {
		return cmp.Compare(queued.arrived(), arrival)
	})
	return slices.Insert(queue, index, item)
}

// egressChoice is the one reference a candidate uses: its own, or a source it derives from.
type egressChoice struct {
	pending *egressExchange
	derived bool
}

// egressWaiter is a candidate waiting for a matching reference.
type egressWaiter struct {
	arrival uint64
	// hash and source are the queues it waits in, and chosen is set once a reference
	// is delivered. Callers hold the queue's mu for all three.
	hash   comparison.Hash
	source Option[comparison.Hash]
	chosen bool
	// ready has one slot and receives at most one reference, so delivery never blocks.
	ready chan egressChoice
}

func newEgressWaiter(arrival uint64) *egressWaiter {
	return &egressWaiter{arrival: arrival, ready: make(chan egressChoice, 1)}
}

func (w *egressWaiter) arrived() uint64 {
	return w.arrival
}

// waitIn records the queues the waiter waits in.
func (w *egressWaiter) waitIn(hash comparison.Hash, source Option[comparison.Hash]) {
	w.hash, w.source = hash, source
}

func (w *egressWaiter) queues() (hash comparison.Hash, source Option[comparison.Hash]) {
	return w.hash, w.source
}

func (w *egressWaiter) isChosen() bool {
	return w.chosen
}

func (w *egressWaiter) deliver(choice egressChoice) {
	w.chosen = true
	w.ready <- choice
}

func (w *egressWaiter) delivered() <-chan egressChoice {
	return w.ready
}

// egressOutcome is how a reference request finished.
type egressOutcome struct {
	raw     Option[Raw]
	failure Option[error]
	// decoded is a source's response, which derivations read after its replay.
	decoded Option[Content]
}

// egressExchange is one forwarded reference request.
type egressExchange struct {
	arrival uint64
	// source reports that derivations read the decoded response.
	source  bool
	outcome *future.Future[egressOutcome]

	mu        sync.Mutex
	abandoned bool
	expiry    Option[*time.Timer]
	// holders counts derivations reading the decoded response, plus one until the
	// source is withdrawn. Its hold is released when none remain.
	holders   int
	withdrawn bool
}

func newEgressExchange(arrival uint64, source bool) *egressExchange {
	holders := 0
	if source {
		holders = 1
	}
	return &egressExchange{arrival: arrival, source: source, outcome: future.New[egressOutcome](), holders: holders}
}

func (e *egressExchange) arrived() uint64 {
	return e.arrival
}

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

// finish records the reference outcome, releasing whatever nobody can use any more.
func (e *egressExchange) finish(raw Option[Raw], failure Option[error], decoded Option[Content]) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.outcome.Set(egressOutcome{raw: raw, failure: failure, decoded: decoded})
	if e.abandoned {
		e.release()
	}
	if e.holders == 0 {
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

// hold keeps the decoded response for one more derivation.
func (e *egressExchange) hold() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.holders++
}

// unhold ends one derivation's use of the decoded response.
func (e *egressExchange) unhold() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.holders--
	if e.holders == 0 {
		e.releaseHold()
	}
}

// withdraw ends the source's offer to derivations. Only the first call has any effect.
func (e *egressExchange) withdraw() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.source || e.withdrawn {
		return
	}
	e.withdrawn = true
	e.holders--
	if e.holders == 0 {
		e.releaseHold()
	}
}

// releaseHold frees the memory reserved for the decoded response, if it is decoded yet. Callers hold e.mu.
func (e *egressExchange) releaseHold() {
	outcome, finished := e.outcome.Get()
	if !finished {
		return
	}
	if decoded, ok := outcome.decoded.Get(); ok {
		if release, held := decoded.Release.Get(); held {
			release()
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
		return nil, errors.Wrap(failure, "reference response cannot be replayed")
	}
	raw, ok := outcome.raw.Get()
	if !ok {
		return nil, errors.New("reference response is missing")
	}
	return raw, nil
}

// decoded blocks until a source's response is decoded. Giving up never abandons
// the recording, because the source stays claimable as a reference.
func (e *egressExchange) decoded(ctx context.Context) (Content, error) {
	outcome, err := e.outcome.Wait(ctx)
	if err != nil {
		return Content{}, errors.Wrap(err, "wait for source reference response")
	}
	if failure, failed := outcome.failure.Get(); failed {
		return Content{}, errors.Wrap(failure, "source reference response failed")
	}
	decoded, ok := outcome.decoded.Get()
	if !ok {
		return Content{}, errors.New("source reference response is not decoded")
	}
	return decoded, nil
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
	r.queue.finish(ctx, pending, response)
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

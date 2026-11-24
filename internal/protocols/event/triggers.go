package event

import (
	"sync"

	"github.com/block/spectre/spectrepb/eventpb"
)

// triggers queues runs for the candidate's Receive streams. A run is offered only while a
// stream is open, so the reference never gets a run ID that no candidate can receive.
type triggers struct {
	queue chan *eventpb.ReceiveResponse
	mu    sync.Mutex
	// receivers counts the open Receive streams.
	receivers int
}

func newTriggers(capacity int) *triggers {
	return &triggers{queue: make(chan *eventpb.ReceiveResponse, capacity)}
}

// offer queues a run, reporting false when no stream is open or the queue is full.
func (t *triggers) offer(trigger *eventpb.ReceiveResponse) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.receivers == 0 {
		return false
	}
	select {
	case t.queue <- trigger:
		return true
	default:
		return false
	}
}

// open records a Receive stream, and returns the queue it takes runs from.
func (t *triggers) open() <-chan *eventpb.ReceiveResponse {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.receivers++
	return t.queue
}

// close records that a Receive stream ended. When the last one ends, the queued runs are
// dropped, because a later stream would start them too late to pair with the reference.
func (t *triggers) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.receivers--
	if t.receivers > 0 {
		return
	}
	for {
		select {
		case <-t.queue:
		default:
			return
		}
	}
}

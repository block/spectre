package egress

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison"
)

// recorder pairs candidate requests with reference recordings of the same hash.
// Each recording is claimed at most once, oldest first, so duplicates pair one-to-one.
type recorder struct {
	window time.Duration

	// mu guards both queues. A hash never has unused recordings and waiting
	// candidates at the same time, because add hands over to a waiter first.
	mu      sync.Mutex
	unused  map[comparison.RequestHash][]*recording
	waiting map[comparison.RequestHash][]chan *recording
}

func newRecorder(window time.Duration) *recorder {
	return &recorder{
		window:  window,
		unused:  make(map[comparison.RequestHash][]*recording),
		waiting: make(map[comparison.RequestHash][]chan *recording),
	}
}

// add offers a reference recording to the oldest waiting candidate, or keeps it
// unused for the window before releasing it.
func (r *recorder) add(hash comparison.RequestHash, recorded *recording) {
	r.mu.Lock()
	if waiters := r.waiting[hash]; len(waiters) > 0 {
		r.setWaiting(hash, waiters[1:])
		r.mu.Unlock()
		// Each waiter channel has one slot and receives at most one recording.
		waiters[0] <- recorded
		return
	}
	r.unused[hash] = append(r.unused[hash], recorded)
	r.mu.Unlock()
	time.AfterFunc(r.window, func() { r.expire(hash, recorded) })
}

// claim takes the oldest unused recording for hash, waiting up to the window
// for a reference request to arrive.
func (r *recorder) claim(ctx context.Context, hash comparison.RequestHash) (*recording, error) {
	r.mu.Lock()
	if unused := r.unused[hash]; len(unused) > 0 {
		r.setUnused(hash, unused[1:])
		r.mu.Unlock()
		return unused[0], nil
	}
	waiter := make(chan *recording, 1)
	r.waiting[hash] = append(r.waiting[hash], waiter)
	r.mu.Unlock()

	timer := time.NewTimer(r.window)
	defer timer.Stop()
	var err error
	select {
	case recorded := <-waiter:
		return recorded, nil
	case <-timer.C:
		err = errors.Errorf("no matching reference request within %s", r.window)
	case <-ctx.Done():
		err = errors.Wrap(ctx.Err(), "wait for matching reference request")
	}
	r.mu.Lock()
	waiters := r.waiting[hash]
	index := slices.Index(waiters, waiter)
	if index >= 0 {
		r.setWaiting(hash, slices.Delete(waiters, index, index+1))
	}
	r.mu.Unlock()
	if index < 0 {
		// add removed this waiter concurrently, so a recording is already on its way.
		return <-waiter, nil
	}
	return nil, err
}

// expire releases a recording that no candidate claimed within the window.
func (r *recorder) expire(hash comparison.RequestHash, recorded *recording) {
	r.mu.Lock()
	unused := r.unused[hash]
	index := slices.Index(unused, recorded)
	if index >= 0 {
		r.setUnused(hash, slices.Delete(unused, index, index+1))
	}
	r.mu.Unlock()
	if index >= 0 {
		recorded.release()
	}
}

// setUnused replaces the unused queue for hash. Callers hold r.mu.
func (r *recorder) setUnused(hash comparison.RequestHash, unused []*recording) {
	if len(unused) == 0 {
		delete(r.unused, hash)
		return
	}
	r.unused[hash] = unused
}

// setWaiting replaces the waiting queue for hash. Callers hold r.mu.
func (r *recorder) setWaiting(hash comparison.RequestHash, waiters []chan *recording) {
	if len(waiters) == 0 {
		delete(r.waiting, hash)
		return
	}
	r.waiting[hash] = waiters
}

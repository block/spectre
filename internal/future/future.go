// Package future provides a value that one goroutine sets once and others wait for.
package future

import (
	"context"
	"sync"

	"github.com/alecthomas/errors"
)

// Future is a value that is set at most once. Once set it never changes, so
// waiters may keep using it without a lock.
type Future[T any] struct {
	// done is closed when the value is set, so waiters can also select on a context.
	done chan struct{}

	mu    sync.Mutex
	ready bool
	value T
}

// New returns a Future that is not set yet.
func New[T any]() *Future[T] {
	return &Future[T]{done: make(chan struct{})}
}

// Set stores value and wakes every waiter. It reports false, keeping the
// first value, if the Future is already set.
func (f *Future[T]) Set(value T) (stored bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ready {
		return false
	}
	f.value, f.ready = value, true
	close(f.done)
	return true
}

// Get returns the value without waiting, or false if it is not set yet.
func (f *Future[T]) Get() (value T, ready bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value, f.ready
}

// Wait blocks until the value is set or ctx is done.
func (f *Future[T]) Wait(ctx context.Context) (T, error) {
	select {
	case <-f.done:
	case <-ctx.Done():
		var zero T
		return zero, errors.WithStack(ctx.Err())
	}
	value, _ := f.Get()
	return value, nil
}

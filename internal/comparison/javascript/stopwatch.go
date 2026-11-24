package javascript

import (
	"context"
	"time"

	"github.com/grafana/sobek"
)

// stopwatch interrupts a runtime once scripts have run for its limit, counting only while
// running. It is confined to the runtime's caller.
type stopwatch struct {
	runtime   *sobek.Runtime
	remaining time.Duration
	running   bool
	started   time.Time
	timer     *time.Timer
}

func newStopwatch(runtime *sobek.Runtime, limit time.Duration) *stopwatch {
	return &stopwatch{runtime: runtime, remaining: limit}
}

// count starts counting until the returned function stops it. While it is already counting,
// as when one callback calls another, the outer count continues and the returned function does nothing.
func (s *stopwatch) count() (stop func()) {
	if s.running {
		return func() {}
	}
	s.resume()
	return s.pause
}

// exclude stops counting until the returned function resumes it, if it was counting.
func (s *stopwatch) exclude() (resume func()) {
	if !s.running {
		return func() {}
	}
	s.pause()
	return s.resume
}

func (s *stopwatch) resume() {
	s.running = true
	s.started = time.Now()
	s.timer = time.AfterFunc(s.remaining, func() { s.runtime.Interrupt(context.DeadlineExceeded) })
}

// pause stops counting. Once the limit has passed, the runtime stays interrupted.
func (s *stopwatch) pause() {
	if !s.running {
		return
	}
	s.running = false
	if s.timer.Stop() {
		s.remaining = max(s.remaining-time.Since(s.started), 0)
	}
}

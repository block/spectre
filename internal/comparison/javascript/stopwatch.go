package javascript

import (
	"context"
	"time"

	"github.com/grafana/sobek"
)

// stopwatch interrupts a runtime once scripts have run for its limit. Time while it is
// paused, such as waiting for a reference reply, does not count. It is confined to the runtime's caller.
type stopwatch struct {
	runtime   *sobek.Runtime
	remaining time.Duration
	started   time.Time
	timer     *time.Timer
}

func startStopwatch(runtime *sobek.Runtime, limit time.Duration) *stopwatch {
	clock := &stopwatch{runtime: runtime, remaining: limit}
	clock.resume()
	return clock
}

// resume starts counting the remaining time again.
func (s *stopwatch) resume() {
	s.started = time.Now()
	s.timer = time.AfterFunc(s.remaining, func() { s.runtime.Interrupt(context.DeadlineExceeded) })
}

// pause stops counting. Once the limit has passed, the runtime stays interrupted.
func (s *stopwatch) pause() {
	if s.timer.Stop() {
		s.remaining = max(s.remaining-time.Since(s.started), 0)
	}
}

// stop stops counting for good.
func (s *stopwatch) stop() {
	s.pause()
}

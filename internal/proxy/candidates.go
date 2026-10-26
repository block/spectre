// Package proxy admits candidate work and budgets shared memory for every protocol.
package proxy

import (
	"context"
	"sync"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/quarantine"
)

// Candidates admits candidate work and tracks it so quarantine and shutdown can
// cancel everything in flight. Quarantine lasts for the process lifetime.
type Candidates struct {
	maxInFlight int

	// mu serializes admission with quarantine and shutdown.
	mu         sync.Mutex
	closing    bool
	quarantine Option[QuarantineStatus]
	// quarantined is closed once quarantine begins.
	quarantined chan struct{}
	runs        map[*CandidateRun]struct{}
	idle        chan struct{}
}

// QuarantineStatus is a quarantine in effect: why it began and when.
type QuarantineStatus struct {
	Reason quarantine.Reason
	Since  time.Time
}

// NewCandidates tracks candidate work, quarantining beyond the configured concurrent runs.
func NewCandidates(config CandidatesConfig) *Candidates {
	idle := make(chan struct{})
	close(idle)
	return &Candidates{
		maxInFlight: config.CandidateMaxInFlight,
		quarantined: make(chan struct{}),
		runs:        make(map[*CandidateRun]struct{}),
		idle:        idle,
	}
}

// CandidateRun is one admitted unit of candidate work. Its pointer gives the
// non-comparable cancellation function a stable map identity.
type CandidateRun struct {
	cancelContext context.CancelFunc
}

func newCandidateRun(cancel context.CancelFunc) *CandidateRun {
	return &CandidateRun{cancelContext: cancel}
}

func (r *CandidateRun) cancel() {
	r.cancelContext()
}

// Start admits candidate work that cancel stops, returning None when it is refused.
// Exceeding the concurrency limit quarantines the candidate.
func (c *Candidates) Start(ctx context.Context, cancel context.CancelFunc) Option[*CandidateRun] {
	run, capacityExceeded := c.admit(cancel)
	if capacityExceeded {
		c.Quarantine(ctx, quarantine.Reason{Kind: quarantine.KindCapacity, Detail: "candidate concurrency limit exceeded"})
	}
	return run
}

func (c *Candidates) admit(cancel context.CancelFunc) (run Option[*CandidateRun], capacityExceeded bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.quarantine.Ok() {
		return None[*CandidateRun](), false
	}
	if len(c.runs) >= c.maxInFlight {
		return None[*CandidateRun](), true
	}
	// Replacing the closed channel before registration keeps Shutdown's snapshot valid.
	if len(c.runs) == 0 {
		c.idle = make(chan struct{})
	}
	admitted := newCandidateRun(cancel)
	c.runs[admitted] = struct{}{}
	return Some(admitted), false
}

// Finish releases an admitted run once all of its work has stopped.
func (c *Candidates) Finish(run *CandidateRun) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.runs, run)
	if len(c.runs) == 0 {
		close(c.idle)
	}
}

// Quarantine refuses all further candidate work and cancels active runs. Only
// the first reason is kept.
func (c *Candidates) Quarantine(ctx context.Context, reason quarantine.Reason) {
	// Publish quarantine before cancelling active runs so concurrent admission
	// cannot send more traffic to the candidate.
	c.mu.Lock()
	if c.quarantine.Ok() {
		c.mu.Unlock()
		return
	}
	c.quarantine = Some(QuarantineStatus{Reason: reason, Since: time.Now()})
	close(c.quarantined)
	runs := c.snapshot()
	c.mu.Unlock()

	// Cancellation and logging may acquire transport or output locks, so keep
	// them off the reference request path that detected the quarantine.
	go func() {
		for _, run := range runs {
			run.cancel()
		}
		logger.FromContext(ctx).ErrorContext(context.WithoutCancel(ctx), "Candidate quarantined", "event", logger.EventQuarantine, "reason", reason.String())
	}()
}

// QuarantineStatus reports the quarantine in effect, if any.
func (c *Candidates) QuarantineStatus() Option[QuarantineStatus] {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.quarantine
}

// Quarantined returns a channel that is closed once quarantine begins.
func (c *Candidates) Quarantined() <-chan struct{} {
	return c.quarantined
}

// Close refuses further candidate work and cancels active runs without waiting.
func (c *Candidates) Close() {
	c.close()
}

// Shutdown refuses further candidate work and waits for active runs to finish.
func (c *Candidates) Shutdown(ctx context.Context) error {
	idle := c.close()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "wait for candidate requests")
	}
}

// close cancels active runs and returns a channel closed once they finish.
func (c *Candidates) close() (idle <-chan struct{}) {
	c.mu.Lock()
	c.closing = true
	idle = c.idle
	runs := c.snapshot()
	c.mu.Unlock()
	// Candidate work is non-critical and must not extend server shutdown.
	for _, run := range runs {
		run.cancel()
	}
	return idle
}

// snapshot copies the active runs. Callers hold c.mu.
func (c *Candidates) snapshot() []*CandidateRun {
	runs := make([]*CandidateRun, 0, len(c.runs))
	for run := range c.runs {
		runs = append(runs, run)
	}
	return runs
}

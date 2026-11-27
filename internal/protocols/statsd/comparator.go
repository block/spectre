package statsd

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/DataDog/sketches-go/ddsketch/mapping"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/statsd/metric"
)

// queuedObservations bounds the datagrams waiting for comparison. Beyond it the window is not judged.
const queuedObservations = 4096

// observation is a datagram from one side, stamped with when it arrived.
type observation struct {
	side     model.Side
	at       time.Time
	datagram []byte
}

// comparator compares both sides' metrics in windows, and quarantines the candidate once a
// compared series is missing or differs beyond its tolerance in enough windows in a row.
type comparator struct {
	config      Config
	resolver    *resolver
	quarantiner engine.Quarantiner
	status      protocols.QuarantineStatus
	log         *slog.Logger
	queue       chan observation
	origin      time.Time
	windows     map[int64]*window
	indexes     mapping.IndexMapping
	totals      *runningTotals
	// streaks are the failing runs of series, by key.
	streaks map[string]streak
	// candidateFrom is when the candidate first sent a compared series.
	candidateFrom Option[time.Time]
	stopped       bool
	lost          *losses
}

func newComparator(ctx context.Context, config Config, resolver *resolver, quarantiner engine.Quarantiner, status protocols.QuarantineStatus) (*comparator, error) {
	indexes, err := newSketchMapping()
	if err != nil {
		return nil, err
	}
	return &comparator{
		config:        config,
		resolver:      resolver,
		quarantiner:   quarantiner,
		status:        status,
		log:           logger.FromContext(ctx),
		queue:         make(chan observation, queuedObservations),
		origin:        time.Now(),
		windows:       map[int64]*window{},
		indexes:       indexes,
		totals:        newRunningTotals(),
		streaks:       map[string]streak{},
		candidateFrom: None[time.Time](),
		lost:          newLosses(),
	}, nil
}

// observe returns the function a side's relay passes each datagram to. It never blocks.
func (c *comparator) observe(side model.Side) func(datagram []byte) {
	return func(datagram []byte) {
		arrived := observation{side: side, at: time.Now(), datagram: bytes.Clone(datagram)}
		select {
		case c.queue <- arrived:
		default:
			c.lost.record(arrived.at)
		}
	}
}

// run compares until ctx ends.
func (c *comparator) run(ctx context.Context) {
	ticker := time.NewTicker(min(c.config.Window/4, time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case arrived := <-c.queue:
			c.add(ctx, arrived)
		case now := <-ticker.C:
			c.judge(ctx, now)
		}
	}
}

// add counts a datagram's samples in the window it arrived in.
func (c *comparator) add(ctx context.Context, arrived observation) {
	if c.stopped {
		return
	}
	samples, err := metric.Parse(arrived.datagram)
	if err != nil {
		c.log.DebugContext(ctx, "Skipped malformed StatsD lines", "side", arrived.side, "error", err)
	}
	series := make([]metric.Series, 0, len(samples))
	for _, sample := range samples {
		series = append(series, sample.Series)
	}
	resolutions, fresh, err := c.resolver.resolve(ctx, arrived.side, series)
	if err != nil {
		c.quarantine(ctx, model.QuarantineReason{Kind: model.QuarantineUnable, Detail: err.Error()})
		return
	}
	for _, index := range fresh {
		c.log.DebugContext(ctx, "New StatsD series", "side", arrived.side, "series", series[index].String(),
			"type", series[index].Type, "outcome", resolutions[index].outcome, "as", resolutions[index].series.String())
	}
	if c.resolver.size() > 2*c.config.MaxSeries {
		c.quarantine(ctx, model.QuarantineReason{Kind: model.QuarantineCapacity, Detail: fmt.Sprintf("StatsD sent more than %d distinct series", 2*c.config.MaxSeries)})
		return
	}
	for index, sample := range samples {
		resolved := resolutions[index]
		if resolved.outcome == ignored {
			continue
		}
		if arrived.side == model.Candidate && resolved.outcome == compared && !c.candidateFrom.Ok() {
			c.candidateFrom = Some(arrived.at)
			c.log.InfoContext(ctx, "Candidate sent its first compared StatsD metric", "series", resolved.series.Route())
		}
		target := c.window(arrived.at)
		if target.add(arrived.side, sample, resolved) && target.size() > c.config.MaxSeries {
			c.quarantine(ctx, model.QuarantineReason{Kind: model.QuarantineCapacity, Detail: fmt.Sprintf("StatsD sent more than %d series in a window", c.config.MaxSeries)})
			return
		}
	}
}

// window returns the window that at falls in.
func (c *comparator) window(at time.Time) *window {
	index := int64(at.Sub(c.origin) / c.config.Window)
	found, ok := c.windows[index]
	if !ok {
		found = newWindow(c.origin.Add(time.Duration(index)*c.config.Window), c.indexes)
		c.windows[index] = found
	}
	return found
}

// judge settles each window that ended at least the settle delay before now. Windows before
// the candidate starts sending compared metrics, or within its startup timeout, are dropped.
func (c *comparator) judge(ctx context.Context, now time.Time) {
	if c.stopped {
		return
	}
	if c.status.QuarantineStatus().Ok() {
		c.stopped = true
		return
	}
	c.markLossy()
	from, starts := c.candidateFrom.Get()
	if deadline := c.origin.Add(c.config.CandidateStartupTimeout); !starts || deadline.Before(from) {
		from = deadline
	}
	for _, index := range slices.Sorted(maps.Keys(c.windows)) {
		settled := c.windows[index]
		if settled.started().Add(c.config.Window + c.config.Settle).After(now) {
			return
		}
		delete(c.windows, index)
		if settled.started().Before(from) {
			continue
		}
		if settled.dropped() {
			c.log.WarnContext(ctx, "Skipped a StatsD window that dropped datagrams, and restarted the running totals", "start", settled.started())
			c.totals.reset()
			clear(c.streaks)
			continue
		}
		if reason, failed := c.verdict(settled).Get(); failed {
			c.quarantine(ctx, reason)
			return
		}
	}
}

// verdict extends each series' failing run with the window, and returns the first failure that
// quarantines: a rule that cannot judge a series at once, a difference once it persists.
func (c *comparator) verdict(settled *window) Option[model.QuarantineReason] {
	for _, judged := range settled.judge(c.totals, c.config.MinTotal) {
		reason, failed := judged.reason.Get()
		switch {
		case !failed:
			delete(c.streaks, judged.key)
		case reason.Kind == model.QuarantineUnable:
			return Some(c.during(reason, streak{failures: 1, from: settled.started()}))
		default:
			run := c.streaks[judged.key]
			if run.failures == 0 {
				run.from = settled.started()
			}
			run.failures++
			c.streaks[judged.key] = run
			if run.failures >= c.config.FailingWindows {
				return Some(c.during(reason, run))
			}
		}
	}
	return None[model.QuarantineReason]()
}

// during says which windows the reason's failure ran over.
func (c *comparator) during(reason model.QuarantineReason, run streak) model.QuarantineReason {
	windows := fmt.Sprintf("the %s window", c.config.Window)
	if run.failures > 1 {
		windows = fmt.Sprintf("%d successive %s windows", run.failures, c.config.Window)
	}
	reason.Detail = fmt.Sprintf("%s, in %s from %s", reason.Detail, windows, run.from.UTC().Format(time.TimeOnly))
	return reason
}

// streak is a series' run of failing windows.
type streak struct {
	failures int
	from     time.Time
}

// markLossy marks the windows that lost datagrams.
func (c *comparator) markLossy() {
	for _, at := range c.lost.take() {
		c.window(at).markDropped()
	}
}

// quarantine quarantines the candidate and stops comparing.
func (c *comparator) quarantine(ctx context.Context, reason model.QuarantineReason) {
	c.stopped = true
	c.quarantiner.Quarantine(ctx, reason)
}

// losses are the arrival times of datagrams the comparison queue had no room for.
type losses struct {
	mu    sync.Mutex
	times []time.Time
}

func newLosses() *losses {
	return &losses{}
}

// record notes a datagram lost at at.
func (l *losses) record(at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.times = append(l.times, at)
}

// take returns the times recorded since the last take.
func (l *losses) take() []time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	taken := l.times
	l.times = nil
	return taken
}

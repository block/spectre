package statsd

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/DataDog/sketches-go/ddsketch/mapping"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/statsd/metric"
	"github.com/block/spectre/internal/protocols/statsd/scripted"
)

// window holds both sides' series that arrived in one interval.
type window struct {
	start time.Time
	// lossy windows dropped datagrams, so they are not judged.
	lossy   bool
	series  map[string]*windowSeries
	indexes mapping.IndexMapping
}

func newWindow(start time.Time, indexes mapping.IndexMapping) *window {
	return &window{start: start, series: map[string]*windowSeries{}, indexes: indexes}
}

// add counts a sample under its resolution, and reports whether the series is new to the window.
func (w *window) add(side model.Side, sample metric.Sample, resolved resolution) (added bool) {
	key := resolved.series.Key()
	entry, found := w.series[key]
	if !found {
		entry = newWindowSeries(resolved, w.indexes)
		w.series[key] = entry
	}
	entry.add(side, sample, resolved.scale)
	return !found
}

// started is when the window starts.
func (w *window) started() time.Time {
	return w.start
}

// size is how many series the window holds.
func (w *window) size() int {
	return len(w.series)
}

// markDropped notes that the window lost datagrams.
func (w *window) markDropped() {
	w.lossy = true
}

// dropped reports whether the window lost datagrams.
func (w *window) dropped() bool {
	return w.lossy
}

// judgement is one series' verdict in a window.
type judgement struct {
	key    string
	reason Option[model.QuarantineReason]
}

// judge adds the window's totals and counts to the running totals, then judges each series
// in key order. Running totals are compared once either side's reaches minimum.
func (w *window) judge(totals *runningTotals, minimum float64) []judgement {
	keys := slices.Sorted(maps.Keys(w.series))
	for _, key := range keys {
		w.series[key].accumulate(key, totals)
	}
	judgements := make([]judgement, 0, len(keys))
	for _, key := range keys {
		judgements = append(judgements, judgement{key: key, reason: w.series[key].verdict(totals.sides(key), minimum)})
	}
	return judgements
}

// windowSeries is one mapped series in a window, with each side's values.
type windowSeries struct {
	resolution resolution
	sides      map[model.Side]*aggregate
	indexes    mapping.IndexMapping
}

func newWindowSeries(resolved resolution, indexes mapping.IndexMapping) *windowSeries {
	return &windowSeries{resolution: resolved, sides: map[model.Side]*aggregate{}, indexes: indexes}
}

func (s *windowSeries) add(side model.Side, sample metric.Sample, scale float64) {
	values, found := s.sides[side]
	if !found {
		values = newAggregate(s.resolution.series.Type.Class(), s.indexes)
		s.sides[side] = values
	}
	values.add(sample, scale)
}

// accumulate adds each side's total or count to the running totals, if the rule compares it.
func (s *windowSeries) accumulate(key string, totals *runningTotals) {
	measure := s.resolution.series.Type.Class().Measure()
	if s.resolution.outcome != compared || !measure.Accumulates() {
		return
	}
	for side, values := range s.sides {
		totals.add(key, side, values.statistic(measure))
	}
}

// verdict is why the series quarantines the candidate, if it does. Totals and counts are
// judged on the sides' running totals. Reasons never quote values.
func (s *windowSeries) verdict(running map[model.Side]float64, minimum float64) Option[model.QuarantineReason] {
	series := s.resolution.series
	if s.resolution.outcome == uncovered {
		sender := model.Reference
		if _, fromReference := s.sides[model.Reference]; !fromReference {
			sender = model.Candidate
		}
		return s.fail(model.QuarantineUnable, nil, "no StatsD rule compares or ignores %s %s from the %s", series.Type, series.Route(), sender)
	}
	class := series.Type.Class()
	checks := s.resolution.comparison.For(class)
	if len(checks) == 0 {
		return s.fail(model.QuarantineUnable, nil, "the StatsD rule compares %s, which %s %s does not have", joinStatistics(s.resolution.comparison.Statistics()), class, series.Route())
	}
	for _, values := range s.sides {
		if values.overflowed() && slices.ContainsFunc(checks, isPercentile) {
			return s.fail(model.QuarantineUnable, nil, "%s %s has a scaled value too large to take percentiles of", class, series.Route())
		}
	}
	reference, hasReference := s.sides[model.Reference]
	candidate, hasCandidate := s.sides[model.Candidate]
	bothSent := hasReference && hasCandidate
	if !bothSent && !slices.ContainsFunc(checks, accumulates) {
		return s.presence(checks)
	}
	differences, exceeded := []model.Difference{}, []string{}
	for _, check := range checks {
		failure, label := None[string](), string(check.Statistic)
		switch {
		case check.Statistic.Accumulates():
			if max(math.Abs(running[model.Reference]), math.Abs(running[model.Candidate])) >= minimum {
				failure, label = check.Tolerance.Exceeds(running[model.Reference], running[model.Candidate]), "running "+label
			}
		case bothSent:
			failure = check.Tolerance.Exceeds(reference.statistic(check.Statistic), candidate.statistic(check.Statistic))
		}
		if bound, exceeds := failure.Get(); exceeds {
			differences = append(differences, model.Difference{Path: string(check.Statistic), Change: model.ChangeChanged, Count: 1})
			exceeded = append(exceeded, label+" "+bound)
		}
	}
	if len(differences) == 0 {
		return None[model.QuarantineReason]()
	}
	reasons := strings.Join(exceeded, ", ")
	_, referenceSent := running[model.Reference]
	_, candidateSent := running[model.Candidate]
	switch {
	case bothSent:
	case !referenceSent:
		return s.fail(model.QuarantineUnmatched, nil, "candidate sent %s %s that the reference did not: %s", class, series.Route(), reasons)
	case !candidateSent:
		return s.fail(model.QuarantineUnmatched, nil, "candidate left out %s %s that the reference sent: %s", class, series.Route(), reasons)
	}
	return s.fail(model.QuarantineDivergent, differences, "%s %s: %s", class, series.Route(), reasons)
}

// presence judges a series that only one side sent in the window, and whose rule checks no
// running total. A set's members check decides, with the other side's as 0; otherwise it fails.
func (s *windowSeries) presence(checks []scripted.Check) Option[model.QuarantineReason] {
	series := s.resolution.series
	class := series.Type.Class()
	measure := class.Measure()
	sent, fromReference := s.sides[model.Reference]
	if !fromReference {
		return s.fail(model.QuarantineUnmatched, nil, "candidate sent %s %s that the reference did not", class, series.Route())
	}
	because := ""
	if class != metric.Levelled {
		because = fmt.Sprintf("; the rule checks no %s", measure)
		for _, check := range checks {
			if check.Statistic != measure {
				continue
			}
			bound, exceeds := check.Tolerance.Exceeds(sent.statistic(measure), 0).Get()
			if !exceeds {
				return None[model.QuarantineReason]()
			}
			because = fmt.Sprintf(": %s %s", measure, bound)
		}
	}
	return s.fail(model.QuarantineUnmatched, nil, "candidate left out %s %s that the reference sent%s", class, series.Route(), because)
}

// fail is a quarantine of the series' traffic.
func (s *windowSeries) fail(kind model.QuarantineKind, differences []model.Difference, format string, arguments ...any) Option[model.QuarantineReason] {
	traffic := Some(model.Traffic{Direction: model.Egress, Protocol: scripted.Protocol, Route: s.resolution.series.Route()})
	return Some(model.QuarantineReason{Kind: kind, Traffic: traffic, Differences: differences, Detail: fmt.Sprintf(format, arguments...)})
}

func accumulates(check scripted.Check) bool {
	return check.Statistic.Accumulates()
}

func isPercentile(check scripted.Check) bool {
	_, percentile := check.Statistic.Rank()
	return percentile
}

// joinStatistics lists statistics, as in "count and p95".
func joinStatistics(statistics []metric.Statistic) string {
	names := make([]string, 0, len(statistics))
	for _, statistic := range statistics {
		names = append(names, string(statistic))
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

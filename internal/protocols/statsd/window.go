package statsd

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/statsd/metric"
	"github.com/block/spectre/internal/protocols/statsd/scripted"
)

// window holds both sides' series that arrived in one interval.
type window struct {
	start time.Time
	// lossy windows dropped datagrams, so they are not judged.
	lossy  bool
	series map[string]*windowSeries
}

func newWindow(start time.Time) *window {
	return &window{start: start, series: map[string]*windowSeries{}}
}

// add counts a sample under its resolution, and reports whether the series is new to the window.
func (w *window) add(side model.Side, sample metric.Sample, resolved resolution) (added bool) {
	key := resolved.series.Key()
	entry, found := w.series[key]
	if !found {
		entry = newWindowSeries(resolved)
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

// verdict is the first failure in the window, in series order.
func (w *window) verdict(length time.Duration) Option[model.QuarantineReason] {
	for _, key := range slices.Sorted(maps.Keys(w.series)) {
		if reason, failed := w.series[key].verdict().Get(); failed {
			reason.Detail = fmt.Sprintf("%s, in the %s window from %s", reason.Detail, length, w.start.UTC().Format(time.TimeOnly))
			return Some(reason)
		}
	}
	return None[model.QuarantineReason]()
}

// windowSeries is one mapped series in a window, with each side's values.
type windowSeries struct {
	resolution resolution
	sides      map[model.Side]*aggregate
}

func newWindowSeries(resolved resolution) *windowSeries {
	return &windowSeries{resolution: resolved, sides: map[model.Side]*aggregate{}}
}

func (s *windowSeries) add(side model.Side, sample metric.Sample, scale float64) {
	values, found := s.sides[side]
	if !found {
		values = newAggregate(s.resolution.series.Type.Class())
		s.sides[side] = values
	}
	values.add(sample, scale)
}

// verdict is why the series quarantines the candidate, if it does. Reasons name the
// series by its route and the rule's bounds, never by values.
func (s *windowSeries) verdict() Option[model.QuarantineReason] {
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
	reference, hasReference := s.sides[model.Reference]
	candidate, hasCandidate := s.sides[model.Candidate]
	if !hasReference || !hasCandidate {
		return s.presence(checks)
	}
	differences, exceeded := []model.Difference{}, []string{}
	for _, check := range checks {
		bound, exceeds := check.Tolerance.Exceeds(reference.statistic(check.Statistic), candidate.statistic(check.Statistic)).Get()
		if exceeds {
			differences = append(differences, model.Difference{Path: string(check.Statistic), Change: model.ChangeChanged, Count: 1})
			exceeded = append(exceeded, fmt.Sprintf("%s %s", check.Statistic, bound))
		}
	}
	if len(differences) == 0 {
		return None[model.QuarantineReason]()
	}
	return s.fail(model.QuarantineDivergent, differences, "%s %s: %s", class, series.Route(), strings.Join(exceeded, ", "))
}

// presence judges a series that only one side sent. The other side's measure counts as 0,
// so the measure's check decides; without one, and for gauges, the series fails.
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

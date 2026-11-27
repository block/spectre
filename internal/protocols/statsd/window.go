package statsd

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
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
func (w *window) add(side metric.Side, sample metric.Sample, resolved resolution) (added bool) {
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
	sides      map[metric.Side]*aggregate
}

func newWindowSeries(resolved resolution) *windowSeries {
	return &windowSeries{resolution: resolved, sides: map[metric.Side]*aggregate{}}
}

func (s *windowSeries) add(side metric.Side, sample metric.Sample, scale float64) {
	values, found := s.sides[side]
	if !found {
		values = newAggregate(s.resolution.series.Type.Class())
		s.sides[side] = values
	}
	values.add(sample, scale)
}

// verdict is why the series quarantines the candidate, if it does.
func (s *windowSeries) verdict() Option[model.QuarantineReason] {
	series := s.resolution.series
	if s.resolution.outcome == uncovered {
		sender := metric.Reference
		if _, fromReference := s.sides[metric.Reference]; !fromReference {
			sender = metric.Candidate
		}
		return s.fail(model.QuarantineUnable, "no StatsD rule compares or ignores %s %s from the %s", series.Type, series, sender)
	}
	class := series.Type.Class()
	checks := s.resolution.comparison.For(class)
	if len(checks) == 0 {
		return s.fail(model.QuarantineUnable, "the StatsD rule compares %s, which %s %s does not have", joinStatistics(s.resolution.comparison.Statistics()), class, series)
	}
	reference, hasReference := s.sides[metric.Reference]
	candidate, hasCandidate := s.sides[metric.Candidate]
	if !hasReference || !hasCandidate {
		return s.presence(checks)
	}
	for _, check := range checks {
		referenceValue, candidateValue := reference.statistic(check.Statistic), candidate.statistic(check.Statistic)
		if !check.Tolerance.Allows(referenceValue, candidateValue) {
			return s.fail(model.QuarantineDivergent, "%s %s %s is %g on the candidate and %g on the reference, %s; the rule allows %s",
				class, series, check.Statistic, candidateValue, referenceValue, difference(referenceValue, candidateValue), check.Tolerance)
		}
	}
	return None[model.QuarantineReason]()
}

// presence judges a series that only one side sent. The other side's measure counts as 0,
// so the measure's check decides; without one, and for gauges, the series fails.
func (s *windowSeries) presence(checks []scripted.Check) Option[model.QuarantineReason] {
	series := s.resolution.series
	class := series.Type.Class()
	measure := class.Measure()
	sent, fromReference := s.sides[metric.Reference]
	if !fromReference {
		sent = s.sides[metric.Candidate]
	}
	value := sent.statistic(measure)
	allowance := "both sides must send a gauge"
	if class != metric.Levelled {
		allowance = fmt.Sprintf("the rule checks no %s, so both sides must send it", measure)
		for _, check := range checks {
			if check.Statistic != measure {
				continue
			}
			referenceValue, candidateValue := value, 0.0
			if !fromReference {
				referenceValue, candidateValue = 0, value
			}
			if check.Tolerance.Allows(referenceValue, candidateValue) {
				return None[model.QuarantineReason]()
			}
			allowance = "the rule allows " + check.Tolerance.String()
		}
	}
	if fromReference {
		return s.fail(model.QuarantineUnmatched, "candidate left out %s %s, which the reference sent with %s %g; %s", class, series, measure, value, allowance)
	}
	return s.fail(model.QuarantineUnmatched, "candidate sent %s %s with %s %g, which the reference did not send; %s", class, series, measure, value, allowance)
}

// fail is a quarantine of the series' traffic.
func (s *windowSeries) fail(kind model.QuarantineKind, format string, arguments ...any) Option[model.QuarantineReason] {
	traffic := Some(model.Traffic{Direction: model.Egress, Protocol: scripted.Protocol, Route: s.resolution.series.Route()})
	return Some(model.QuarantineReason{Kind: kind, Traffic: traffic, Detail: fmt.Sprintf(format, arguments...)})
}

// difference says how far candidate is from reference, as in "36% above".
func difference(reference, candidate float64) string {
	direction, gap := "above", candidate-reference
	if gap < 0 {
		direction, gap = "below", -gap
	}
	if reference == 0 {
		return strconv.FormatFloat(gap, 'g', 4, 64) + " " + direction
	}
	return strconv.FormatFloat(gap/math.Abs(reference)*100, 'g', 3, 64) + "% " + direction
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

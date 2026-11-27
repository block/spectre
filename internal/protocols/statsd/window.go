package statsd

import (
	"fmt"
	"maps"
	"slices"
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
	traffic := Some(model.Traffic{Direction: model.Egress, Protocol: scripted.Protocol, Route: series.Route()})
	fail := func(kind model.QuarantineKind, format string, arguments ...any) Option[model.QuarantineReason] {
		return Some(model.QuarantineReason{Kind: kind, Traffic: traffic, Detail: fmt.Sprintf(format, arguments...)})
	}
	if s.resolution.outcome == uncovered {
		sender := metric.Reference
		if _, fromReference := s.sides[metric.Reference]; !fromReference {
			sender = metric.Candidate
		}
		return fail(model.QuarantineUnable, "no StatsD rule compares or ignores %s %s from the %s", series.Type, series, sender)
	}
	reference, hasReference := s.sides[metric.Reference]
	candidate, hasCandidate := s.sides[metric.Candidate]
	class := series.Type.Class()
	presenceOnly := class == metric.Levelled
	referenceValue, candidateValue := 0.0, 0.0
	if hasReference {
		referenceValue = reference.value()
	}
	if hasCandidate {
		candidateValue = candidate.value()
	}
	comparison := s.resolution.comparison
	if (presenceOnly && hasReference != hasCandidate) || !comparison.Tolerance.Allows(referenceValue, candidateValue) {
		switch {
		case !hasCandidate:
			return fail(model.QuarantineUnmatched, "candidate left out %s %s, which the reference sent with %s %g", class, series, measure(class), referenceValue)
		case !hasReference:
			return fail(model.QuarantineUnmatched, "candidate sent %s %s with %s %g, which the reference did not send", class, series, measure(class), candidateValue)
		default:
			return fail(model.QuarantineDivergent, "%s %s %s is %g on the candidate and %g on the reference", class, series, measure(class), candidateValue, referenceValue)
		}
	}
	if class != metric.Sampled || !hasReference || !hasCandidate {
		return None[model.QuarantineReason]()
	}
	for _, percentile := range comparison.Percentiles {
		referencePercentile, candidatePercentile := reference.percentile(percentile.Rank), candidate.percentile(percentile.Rank)
		if !percentile.Tolerance.Allows(referencePercentile, candidatePercentile) {
			return fail(model.QuarantineDivergent, "distribution %s p%g is %g on the candidate and %g on the reference", series, percentile.Rank, candidatePercentile, referencePercentile)
		}
	}
	return None[model.QuarantineReason]()
}

// measure names the value that a class's tolerance applies to.
func measure(class metric.Class) string {
	switch class {
	case metric.Sampled:
		return "sample count"
	case metric.Levelled:
		return "mean"
	case metric.Distinct:
		return "member count"
	case metric.Counted:
	}
	return "total"
}

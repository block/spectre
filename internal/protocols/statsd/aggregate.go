package statsd

import (
	"github.com/block/spectre/internal/protocols/statsd/metric"
)

// aggregate is one side's values of one series in one window.
type aggregate struct {
	class metric.Class
	// total is a counter's sum, a distribution's sample count, or a gauge's sum of values.
	total float64
	// gauges counts a gauge's values, so total/gauges is their mean.
	gauges  float64
	members map[string]bool
	sketch  *sketch
}

func newAggregate(class metric.Class) *aggregate {
	return &aggregate{class: class, members: map[string]bool{}, sketch: newSketch()}
}

// add counts a sample's values, each multiplied by scale and weighted by the sample rate.
func (a *aggregate) add(sample metric.Sample, scale float64) {
	weight := sample.Weight()
	for _, value := range sample.Values {
		value *= scale
		switch a.class {
		case metric.Counted:
			a.total += value * weight
		case metric.Sampled:
			a.total += weight
			a.sketch.add(value, weight)
		case metric.Levelled:
			a.total += value
			a.gauges++
		case metric.Distinct:
		}
	}
	for _, member := range sample.Members {
		a.members[member] = true
	}
}

// value is what a comparison's tolerance applies to: a counter's total, a distribution's
// sample count, a gauge's mean, or a set's size.
func (a *aggregate) value() float64 {
	switch a.class {
	case metric.Levelled:
		if a.gauges == 0 {
			return 0
		}
		return a.total / a.gauges
	case metric.Distinct:
		return float64(len(a.members))
	case metric.Counted, metric.Sampled:
	}
	return a.total
}

// percentile is a distribution's percentile, by rank from 0 to 100.
func (a *aggregate) percentile(rank float64) float64 {
	return a.sketch.percentile(rank)
}

package statsd

import (
	"github.com/DataDog/sketches-go/ddsketch"
	"github.com/DataDog/sketches-go/ddsketch/mapping"
	"github.com/DataDog/sketches-go/ddsketch/store"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/protocols/statsd/metric"
)

// sketchAccuracy is the relative error of a distribution's percentiles.
const sketchAccuracy = 0.01

// newSketchMapping returns the bucket mapping that every window's sketches share.
func newSketchMapping() (mapping.IndexMapping, error) {
	indexes, err := mapping.NewDefaultMapping(sketchAccuracy)
	return indexes, errors.Wrap(err, "create percentile sketch mapping")
}

// aggregate is one side's values of one series in one window.
type aggregate struct {
	class metric.Class
	// total is a counter's sum, a distribution's sample count, or a gauge's sum of values.
	total float64
	// gauges counts a gauge's values, so total/gauges is their mean.
	gauges  float64
	members map[string]bool
	sketch  *ddsketch.DDSketch
	// untracked is set when a scaled value was too large for the sketch.
	untracked bool
}

func newAggregate(class metric.Class, indexes mapping.IndexMapping) *aggregate {
	return &aggregate{class: class, members: map[string]bool{}, sketch: ddsketch.NewDDSketchFromStoreProvider(indexes, store.DefaultProvider)}
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
			if err := a.sketch.AddWithCount(value, weight); err != nil {
				a.untracked = true
			}
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

// overflowed reports whether a scaled value was too large for the sketch.
func (a *aggregate) overflowed() bool {
	return a.untracked
}

// statistic is one of the window's statistics of the values.
func (a *aggregate) statistic(statistic metric.Statistic) float64 {
	switch statistic {
	case metric.Mean:
		if a.gauges == 0 {
			return 0
		}
		return a.total / a.gauges
	case metric.Members:
		return float64(len(a.members))
	case metric.P50, metric.P75, metric.P90, metric.P95, metric.P99:
		rank, _ := statistic.Rank()
		percentile, err := a.sketch.GetValueAtQuantile(rank / 100)
		if err != nil {
			return 0
		}
		return percentile
	case metric.Total, metric.Count:
	}
	return a.total
}

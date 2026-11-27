package statsd

import (
	"github.com/block/spectre/internal/model"
)

// runningTotals are each side's totals and counts of every compared series since comparison
// started. Mirrored traffic makes them converge, so a window edge matters less as they grow.
type runningTotals struct {
	series map[string]map[model.Side]float64
}

func newRunningTotals() *runningTotals {
	return &runningTotals{series: map[string]map[model.Side]float64{}}
}

// add counts value to a side's running total of the series under key.
func (r *runningTotals) add(key string, side model.Side, value float64) {
	sides, found := r.series[key]
	if !found {
		sides = map[model.Side]float64{}
		r.series[key] = sides
	}
	sides[side] += value
}

// sides are the running totals of the series under key, by the sides that sent it.
func (r *runningTotals) sides(key string) map[model.Side]float64 {
	return r.series[key]
}

// reset forgets every running total.
func (r *runningTotals) reset() {
	clear(r.series)
}

package statsd

import (
	"maps"
	"math"
	"slices"
)

// sketchAccuracy is the relative error of a sketch's percentiles.
const sketchAccuracy = 0.01

// sketch estimates percentiles of weighted values within sketchAccuracy, in logarithmic
// buckets. Values of zero or less share one bucket and read as zero.
type sketch struct {
	gamma   float64
	buckets map[int]float64
	zero    float64
	count   float64
}

func newSketch() *sketch {
	return &sketch{gamma: (1 + sketchAccuracy) / (1 - sketchAccuracy), buckets: map[int]float64{}}
}

// add counts value weight times.
func (s *sketch) add(value, weight float64) {
	s.count += weight
	if value <= 0 {
		s.zero += weight
		return
	}
	s.buckets[int(math.Ceil(math.Log(value)/math.Log(s.gamma)))] += weight
}

// percentile returns the value below which rank percent of the values fall.
func (s *sketch) percentile(rank float64) float64 {
	target := rank / 100 * s.count
	seen := s.zero
	if seen >= target {
		return 0
	}
	indexes := slices.Sorted(maps.Keys(s.buckets))
	for _, index := range indexes {
		seen += s.buckets[index]
		if seen >= target {
			return 2 * math.Pow(s.gamma, float64(index)) / (s.gamma + 1)
		}
	}
	if len(indexes) == 0 {
		return 0
	}
	return 2 * math.Pow(s.gamma, float64(indexes[len(indexes)-1])) / (s.gamma + 1)
}

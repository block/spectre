// Package metric reads StatsD datagrams into samples of named, tagged series.
//
// It understands the DogStatsD format: several metrics per datagram, one per line,
// each with optional values after the first, a sample rate and tags.
package metric

import (
	"maps"
	"slices"
	"strings"

	"github.com/block/spectre/internal/model"
)

// Side is the service that sent a metric.
type Side string

const (
	// Reference is the service being migrated from.
	Reference Side = "reference"
	// Candidate is the service being migrated to.
	Candidate Side = "candidate"
)

// Type is a metric's StatsD type, as scripts name it.
type Type string

const (
	// Counter values add up.
	Counter Type = "counter"
	// Gauge values replace each other.
	Gauge Type = "gauge"
	// Timer values are durations in milliseconds, compared as a distribution.
	Timer Type = "timer"
	// Histogram values are compared as a distribution.
	Histogram Type = "histogram"
	// Distribution values are compared as a distribution.
	Distribution Type = "distribution"
	// Set values are counted once each.
	Set Type = "set"
)

// Class is how a type's values are compared. Timers, histograms and distributions share one.
type Class string

const (
	// Counted values are summed.
	Counted Class = "counter"
	// Sampled values form a distribution, compared by count and percentiles.
	Sampled Class = "distribution"
	// Levelled values are averaged.
	Levelled Class = "gauge"
	// Distinct values are counted once each.
	Distinct Class = "set"
)

// Class returns how the type's values are compared.
func (t Type) Class() Class {
	switch t {
	case Counter:
		return Counted
	case Timer, Histogram, Distribution:
		return Sampled
	case Gauge:
		return Levelled
	case Set:
		return Distinct
	}
	return Counted
}

// Measure is the statistic that says how much a series of the class sent.
func (c Class) Measure() Statistic {
	switch c {
	case Sampled:
		return Count
	case Levelled:
		return Mean
	case Distinct:
		return Members
	case Counted:
	}
	return Total
}

// Series is one metric with one set of tags. A tag without a value has an empty value.
type Series struct {
	Name string
	Type Type
	Tags map[string]string
}

// Key identifies the series: its name, type class and sorted tags.
func (s Series) Key() string {
	var key strings.Builder
	key.WriteString(s.Name)
	key.WriteString("|")
	key.WriteString(string(s.Type.Class()))
	for _, name := range slices.Sorted(maps.Keys(s.Tags)) {
		key.WriteString("|")
		key.WriteString(name)
		key.WriteString(":")
		key.WriteString(s.Tags[name])
	}
	return key.String()
}

// String names the series with its tags, as in name{tag:value,...}.
func (s Series) String() string {
	if len(s.Tags) == 0 {
		return s.Name
	}
	tags := make([]string, 0, len(s.Tags))
	for _, name := range slices.Sorted(maps.Keys(s.Tags)) {
		tags = append(tags, name+":"+s.Tags[name])
	}
	return s.Name + "{" + strings.Join(tags, ",") + "}"
}

// Route names the series in quarantine reasons, as String does.
func (s Series) Route() model.Route {
	return model.Route(s.String())
}

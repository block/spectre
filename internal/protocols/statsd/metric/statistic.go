package metric

// Statistic is a value of a series in a window that a comparison checks.
type Statistic string

const (
	// Total is a counter's sum.
	Total Statistic = "total"
	// Mean is a gauge's mean.
	Mean Statistic = "mean"
	// Members is how many distinct members a set has.
	Members Statistic = "members"
	// Count is how many values a timer, histogram or distribution has.
	Count Statistic = "count"
	// P50 is a distribution's median.
	P50 Statistic = "p50"
	// P75 is a distribution's 75th percentile.
	P75 Statistic = "p75"
	// P90 is a distribution's 90th percentile.
	P90 Statistic = "p90"
	// P95 is a distribution's 95th percentile.
	P95 Statistic = "p95"
	// P99 is a distribution's 99th percentile.
	P99 Statistic = "p99"
)

// Statistics are every statistic, in the order a comparison checks them.
func Statistics() []Statistic {
	return []Statistic{Total, Mean, Members, Count, P50, P75, P90, P95, P99}
}

// Rank is a percentile's rank, from 0 to 100.
func (s Statistic) Rank() (rank float64, percentile bool) {
	switch s {
	case P50:
		return 50, true
	case P75:
		return 75, true
	case P90:
		return 90, true
	case P95:
		return 95, true
	case P99:
		return 99, true
	case Total, Mean, Members, Count:
	}
	return 0, false
}

// Accumulates reports whether the statistic is compared on running totals since comparison
// started, rather than per window.
func (s Statistic) Accumulates() bool {
	return s == Total || s == Count
}

// Fits reports whether series of class have the statistic.
func (s Statistic) Fits(class Class) bool {
	switch s {
	case Total:
		return class == Counted
	case Mean:
		return class == Levelled
	case Members:
		return class == Distinct
	case Count, P50, P75, P90, P95, P99:
		return class == Sampled
	}
	return false
}

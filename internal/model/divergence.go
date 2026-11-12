package model

import (
	"cmp"

	. "github.com/alecthomas/types/optional"
)

// Divergence is the latest quarantine that the mirror of a service environment
// reported for one of its directions. It outlives the mirror run.
type Divergence struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
	// Revision is the commit of the candidate the mirror ran.
	Revision string `json:"revision"`
	// Direction is the proxy that quarantined the candidate.
	Direction  Direction  `json:"direction"`
	Quarantine Quarantine `json:"quarantine"`
}

// CompareDivergences orders divergences by source: service, environment, then
// ingress before egress.
func CompareDivergences(a, b Divergence) int {
	return cmp.Or(
		cmp.Compare(a.Service, b.Service),
		cmp.Compare(a.Environment, b.Environment),
		cmp.Compare(b.Direction, a.Direction),
	)
}

// DivergenceFilter selects mirror states and divergences. An absent field
// matches every value.
type DivergenceFilter struct {
	Service     Option[string]
	Environment Option[string]
	Revision    Option[string]
	// Node matches only quarantines on routes of that migration state node.
	Node Option[string]
}

// Matches reports whether divergence passes every field of the filter.
func (f DivergenceFilter) Matches(divergence Divergence) bool {
	return matches(f.Service, Some(divergence.Service)) &&
		matches(f.Environment, Some(divergence.Environment)) &&
		matches(f.Revision, Some(divergence.Revision)) &&
		matches(f.Node, divergence.Quarantine.Reason.Node)
}

// MatchesState reports whether state passes every field of the filter. A node
// filter needs one of its quarantines to match.
func (f DivergenceFilter) MatchesState(state State) bool {
	if !matches(f.Service, Some(state.Service)) ||
		!matches(f.Environment, Some(state.Environment)) ||
		!matches(f.Revision, Some(state.Revision)) {
		return false
	}
	if _, ok := f.Node.Get(); !ok {
		return true
	}
	for _, quarantine := range state.Quarantines {
		if matches(f.Node, quarantine.Reason.Node) {
			return true
		}
	}
	return false
}

// matches reports whether value passes a filter. An absent filter matches
// every value, including an absent one.
func matches(filter, value Option[string]) bool {
	expected, ok := filter.Get()
	return !ok || value == Some(expected)
}

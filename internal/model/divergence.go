package model

import . "github.com/alecthomas/types/optional"

// Divergence is the quarantine that the mirror of a service environment
// reported for one of its directions.
type Divergence struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
	// Revision is the commit of the candidate the mirror runs.
	Revision string `json:"revision"`
	// Direction is the proxy that quarantined the candidate.
	Direction  Direction  `json:"direction"`
	Quarantine Quarantine `json:"quarantine"`
}

// DivergenceFilter selects divergences. An absent field matches every value.
type DivergenceFilter struct {
	Service     Option[string]
	Environment Option[string]
	Revision    Option[string]
	// Node matches only divergences on routes of that migration state node.
	Node Option[string]
}

// Matches reports whether divergence passes every field of the filter.
func (f DivergenceFilter) Matches(divergence Divergence) bool {
	return matches(f.Service, Some(divergence.Service)) &&
		matches(f.Environment, Some(divergence.Environment)) &&
		matches(f.Revision, Some(divergence.Revision)) &&
		matches(f.Node, divergence.Quarantine.Reason.Node)
}

// matches reports whether value passes a filter. An absent filter matches
// every value, including an absent one.
func matches(filter, value Option[string]) bool {
	expected, ok := filter.Get()
	return !ok || value == Some(expected)
}

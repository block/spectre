package model

import (
	"time"

	. "github.com/alecthomas/types/optional"
)

// State is what the mirror of a service environment runs and holds, as its
// latest heartbeat reported it.
type State struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
	// CandidateCommit is the commit of the candidate the mirror runs.
	CandidateCommit string `json:"candidate_commit"`
	SpectreVersion  string `json:"spectre_version"`
	// Uptime is how long the mirror has run. Without quarantines, the candidate
	// has been healthy that long, because quarantine lasts for the process.
	Uptime time.Duration `json:"uptime"`
	// Quarantines holds the quarantine of each direction whose proxy has one.
	Quarantines map[Direction]Quarantine `json:"quarantines,omitempty"`
}

// StateFilter selects mirror states. An absent field matches every value.
type StateFilter struct {
	Service         Option[string]
	Environment     Option[string]
	CandidateCommit Option[string]
	// Node matches only states holding a quarantine on a route of that migration state node.
	Node Option[string]
}

// Matches reports whether state passes every field of the filter.
func (f StateFilter) Matches(state State) bool {
	if !matches(f.Service, Some(state.Service)) ||
		!matches(f.Environment, Some(state.Environment)) ||
		!matches(f.CandidateCommit, Some(state.CandidateCommit)) {
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

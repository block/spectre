package model

import "time"

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

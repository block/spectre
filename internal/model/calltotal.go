package model

import "time"

// CallTotal adds up the calls of every mirror run of one service environment
// since its count started. It outlives the runs, so redeploys keep it.
type CallTotal struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
	// Since is when the count started.
	Since time.Time  `json:"since"`
	Start TotalStart `json:"start"`
	Calls Calls      `json:"calls,omitzero"`
	// Runs is how many mirror runs counted calls.
	Runs int `json:"runs,omitzero"`
}

// TotalStart is what began a call total.
type TotalStart string

// What can begin a call total.
const (
	// TotalFirstReport is the service environment's first report.
	TotalFirstReport TotalStart = "first report"
	// TotalQuarantine is a quarantine, which restarts the count.
	TotalQuarantine TotalStart = "quarantine"
	// TotalClear is clearing the service environment's divergences.
	TotalClear TotalStart = "clear"
)

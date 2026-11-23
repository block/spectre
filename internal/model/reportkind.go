package model

// ReportKind distinguishes state, call-count, and heartbeat observations.
type ReportKind int

const (
	// StateReport records the current state and its quarantines.
	StateReport ReportKind = iota
	// CallsReport updates cumulative counts without recording quarantines again.
	CallsReport
	// Keepalive refreshes live state without changing its history.
	Keepalive
)

package model

import (
	"maps"
	"time"
)

// CallHistory keeps a total and recent run checkpoints, so reconnects count once.
type CallHistory struct {
	Total   CallTotal           `json:"total"`
	Recent  map[string]RunCalls `json:"recent,omitempty"`
	Updated time.Time           `json:"updated"`
}

// RunCalls holds a run's cumulative report and its contribution since the last reset.
type RunCalls struct {
	Latest  Calls     `json:"latest,omitzero"`
	Counted Calls     `json:"counted,omitzero"`
	Seen    time.Time `json:"seen"`
	Blocked bool      `json:"blocked,omitzero"`
}

// Observed adds only increases in a healthy run's cumulative counts.
// A run returning after a day of silence has forgotten checkpoints and counts again.
func (h CallHistory) Observed(state State, at time.Time) CallHistory {
	if h.Total.Since.IsZero() {
		h.Total = CallTotal{Service: state.Service, Environment: state.Environment, Since: at, Start: TotalFirstReport}
	}
	if at.After(h.Updated) {
		h.Updated = at
	}
	recent := maps.Clone(h.Recent)
	if recent == nil {
		recent = map[string]RunCalls{}
	}
	for id, run := range recent {
		if id != state.Run && run.Seen.Before(at.Add(-24*time.Hour)) {
			delete(recent, id)
		}
	}
	run := recent[state.Run]
	quarantined := len(state.Quarantines) > 0
	run.Blocked = run.Blocked || (state.Run != "" && quarantined)
	if !run.Blocked && !quarantined {
		delta := state.Calls.Minus(run.Latest)
		if run.Counted.IsZero() && !delta.IsZero() {
			h.Total.Runs++
		}
		h.Total.Calls = h.Total.Calls.Plus(delta)
		run.Latest, run.Counted = run.Latest.Plus(delta), run.Counted.Plus(delta)
	}
	if at.After(run.Seen) {
		run.Seen = at
	}
	recent[state.Run] = run
	h.Recent = recent
	return h
}

// Restart resets contributions while retaining each run's reported checkpoint.
func (h CallHistory) Restart(service, environment string, start TotalStart, at time.Time) CallHistory {
	if start != TotalClear && h.Total.Start != TotalFirstReport && !at.After(h.Total.Since) {
		return h
	}
	h.Total = CallTotal{Service: service, Environment: environment, Since: at, Start: start}
	h.Recent = maps.Clone(h.Recent)
	for id, run := range h.Recent {
		run.Counted = Calls{}
		h.Recent[id] = run
	}
	if start == TotalClear || h.Updated.IsZero() {
		h.Updated = at
	}
	return h
}

// Retained forgets history older than the configured inactivity cutoff.
func (h CallHistory) Retained(since time.Time) CallHistory {
	if h.Updated.Before(since) {
		return CallHistory{}
	}
	return h
}

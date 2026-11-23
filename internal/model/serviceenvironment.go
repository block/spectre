package model

import (
	"slices"
	"time"

	. "github.com/alecthomas/types/optional"
)

// ServiceEnvironment holds the live state and recorded history of one service
// in one environment. Its history outlives its reporting mirror.
type ServiceEnvironment struct {
	EnvironmentStatus
	Received time.Time         `json:"received"`
	Cleared  Option[time.Time] `json:"cleared,omitzero"`
	Calls    CallHistory       `json:"calls,omitzero"`
}

// Observed replaces the live state and optionally records its quarantines.
func (e ServiceEnvironment) Observed(state State, at, callsSince time.Time, kind ReportKind) ServiceEnvironment {
	e.Service, e.Environment = state.Service, state.Environment
	e.State, e.Received = Some(state), at
	if kind != Keepalive {
		e.Calls = e.Calls.Retained(callsSince)
		e.Calls = e.Calls.Observed(state, at)
	}
	if kind == StateReport {
		for _, divergence := range state.Divergences() {
			e = e.Recorded(divergence)
		}
	}
	return e
}

// Recorded keeps a divergence unless a later quarantine or clear supersedes it.
// It leaves shared slices unchanged.
func (e ServiceEnvironment) Recorded(divergence Divergence) ServiceEnvironment {
	index := slices.IndexFunc(e.Divergences, func(stored Divergence) bool {
		return stored.Direction == divergence.Direction
	})
	stored := None[Quarantine]()
	if index >= 0 {
		stored = Some(e.Divergences[index].Quarantine)
	}
	if divergence.Quarantine.SupersededBy(stored, e.Cleared) {
		return e
	}
	if previous, ok := stored.Get(); !ok || divergence.Quarantine.Since.After(previous.Since) {
		e.Calls = e.Calls.Restart(e.Service, e.Environment, TotalQuarantine, divergence.Quarantine.Since)
	}
	e.Divergences = slices.Clone(e.Divergences)
	if index >= 0 {
		e.Divergences[index] = divergence
	} else {
		e.Divergences = append(e.Divergences, divergence)
	}
	return e
}

// ClearedAt removes recorded divergences and suppresses quarantines from at or before.
func (e ServiceEnvironment) ClearedAt(at, callsSince time.Time) ServiceEnvironment {
	if previous, ok := e.Cleared.Get(); ok && !at.After(previous) {
		return e
	}
	e.Divergences, e.Cleared = nil, Some(at)
	e.Calls = e.Calls.Retained(callsSince).Restart(e.Service, e.Environment, TotalClear, at)
	return e
}

// Listed projects live state and persistent history into a status response.
func (e ServiceEnvironment) Listed() Status {
	status := Status{Divergences: e.Divergences}
	if state, ok := e.State.Get(); ok {
		if !e.Calls.Total.Since.IsZero() {
			state.Calls = e.Calls.Recent[state.Run].Counted
		}
		status.States = []State{state}
	}
	if !e.Calls.Total.Since.IsZero() {
		status.CallTotals = []CallTotal{e.Calls.Total}
	}
	return status
}

// ForgetBefore removes live state and call history older than their cutoffs.
func (e ServiceEnvironment) ForgetBefore(since, callsSince time.Time) ServiceEnvironment {
	if e.Received.Before(since) {
		e.State = None[State]()
	}
	e.Calls = e.Calls.Retained(callsSince)
	return e
}

// Retained omits records that have neither live state nor recorded history.
func (e ServiceEnvironment) Retained() Option[ServiceEnvironment] {
	if !e.State.Ok() && len(e.Divergences) == 0 && !e.Cleared.Ok() && e.Calls.Total.Since.IsZero() {
		return None[ServiceEnvironment]()
	}
	return Some(e)
}

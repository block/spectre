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
	Received time.Time
	Cleared  Option[time.Time]
}

// Observed replaces the live state and optionally records its quarantines.
func (e ServiceEnvironment) Observed(state State, at time.Time, recordQuarantines bool) ServiceEnvironment {
	e.Service, e.Environment = state.Service, state.Environment
	e.State, e.Received = Some(state), at
	if recordQuarantines {
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
	e.Divergences = slices.Clone(e.Divergences)
	if index >= 0 {
		e.Divergences[index] = divergence
	} else {
		e.Divergences = append(e.Divergences, divergence)
	}
	return e
}

// ClearedAt removes recorded divergences and suppresses quarantines from at or before.
func (e ServiceEnvironment) ClearedAt(at time.Time) ServiceEnvironment {
	e.Divergences, e.Cleared = nil, Some(at)
	return e
}

// ForgetStateBefore removes live state last received before since, retaining history.
func (e ServiceEnvironment) ForgetStateBefore(since time.Time) ServiceEnvironment {
	if e.Received.Before(since) {
		e.State = None[State]()
	}
	return e
}

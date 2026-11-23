package model

import (
	"cmp"
	"maps"
	"slices"

	. "github.com/alecthomas/types/optional"
)

// Status contains live mirror states and recorded divergences from SPECTRE servers.
// It preserves server order and duplicate reports until grouped by service and environment.
type Status struct {
	States      []State
	Divergences []Divergence
	CallTotals  []CallTotal
}

// Filter selects and orders states and divergences by their source.
func (s Status) Filter(filter DivergenceFilter) Status {
	matching := Status{States: []State{}, Divergences: []Divergence{}}
	for _, state := range s.States {
		if filter.MatchesState(state) {
			matching.States = append(matching.States, state)
		}
	}
	for _, divergence := range s.Divergences {
		if filter.Matches(divergence) {
			matching.Divergences = append(matching.Divergences, divergence)
		}
	}
	listed := map[[2]string]bool{}
	for _, state := range matching.States {
		listed[[2]string{state.Service, state.Environment}] = true
	}
	for _, divergence := range matching.Divergences {
		listed[[2]string{divergence.Service, divergence.Environment}] = true
	}
	for _, total := range s.CallTotals {
		if !filter.MatchesServiceEnvironment(total.Service, total.Environment) {
			continue
		}
		if (filter.Revision.Ok() || filter.Node.Ok()) && !listed[[2]string{total.Service, total.Environment}] {
			continue
		}
		matching.CallTotals = append(matching.CallTotals, total)
	}
	slices.SortFunc(matching.CallTotals, func(a, b CallTotal) int {
		return compareEnvironment(a.Service, a.Environment, b.Service, b.Environment)
	})
	slices.SortFunc(matching.States, func(a, b State) int {
		return compareEnvironment(a.Service, a.Environment, b.Service, b.Environment)
	})
	slices.SortFunc(matching.Divergences, func(a, b Divergence) int {
		return cmp.Or(compareEnvironment(a.Service, a.Environment, b.Service, b.Environment), cmp.Compare(b.Direction, a.Direction))
	})
	return matching
}

// Environments groups reports by service and environment, in source order.
// The last state wins; divergences from different servers remain distinct.
func (s Status) Environments() []EnvironmentStatus {
	byKey := map[[2]string]*EnvironmentStatus{}
	get := func(service, environment string) *EnvironmentStatus {
		key := [2]string{service, environment}
		if _, ok := byKey[key]; !ok {
			byKey[key] = &EnvironmentStatus{Service: service, Environment: environment}
		}
		return byKey[key]
	}
	for _, state := range s.States {
		get(state.Service, state.Environment).State = Some(state)
	}
	for _, divergence := range s.Divergences {
		environment := get(divergence.Service, divergence.Environment)
		environment.Divergences = append(environment.Divergences, divergence)
	}
	for _, total := range s.CallTotals {
		get(total.Service, total.Environment).Total = Some(total)
	}
	environments := make([]EnvironmentStatus, 0, len(byKey))
	for environment := range maps.Values(byKey) {
		environments = append(environments, *environment)
	}
	slices.SortFunc(environments, func(a, b EnvironmentStatus) int {
		return compareEnvironment(a.Service, a.Environment, b.Service, b.Environment)
	})
	return environments
}

func compareEnvironment(aService, aEnvironment, bService, bEnvironment string) int {
	return cmp.Or(cmp.Compare(aService, bService), cmp.Compare(aEnvironment, bEnvironment))
}

// Plus combines server responses without grouping their reports.
func (s Status) Plus(other Status) Status {
	return Status{
		States:      append(s.States, other.States...),
		Divergences: append(s.Divergences, other.Divergences...),
		CallTotals:  append(s.CallTotals, other.CallTotals...),
	}
}

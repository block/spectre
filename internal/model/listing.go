package model

import (
	"cmp"
	"maps"
	"slices"

	. "github.com/alecthomas/types/optional"
)

// Listing is the live states and recorded divergences listed by SPECTRE servers.
// It preserves server order and repeated reports until a view groups them.
type Listing struct {
	States      []State
	Divergences []Divergence
}

// Filter selects and orders states and divergences by their source.
func (l Listing) Filter(filter DivergenceFilter) Listing {
	matching := Listing{States: []State{}, Divergences: []Divergence{}}
	for _, state := range l.States {
		if filter.MatchesState(state) {
			matching.States = append(matching.States, state)
		}
	}
	for _, divergence := range l.Divergences {
		if filter.Matches(divergence) {
			matching.Divergences = append(matching.Divergences, divergence)
		}
	}
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
func (l Listing) Environments() []EnvironmentStatus {
	byKey := map[[2]string]*EnvironmentStatus{}
	get := func(service, environment string) *EnvironmentStatus {
		key := [2]string{service, environment}
		if _, ok := byKey[key]; !ok {
			byKey[key] = &EnvironmentStatus{Service: service, Environment: environment}
		}
		return byKey[key]
	}
	for _, state := range l.States {
		get(state.Service, state.Environment).State = Some(state)
	}
	for _, divergence := range l.Divergences {
		environment := get(divergence.Service, divergence.Environment)
		environment.Divergences = append(environment.Divergences, divergence)
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

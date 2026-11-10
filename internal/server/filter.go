package server

import (
	"slices"

	"github.com/block/spectre/internal/model"
)

// filterStates returns the states that filter selects, ordered by service and
// environment.
func filterStates(states []model.State, filter model.DivergenceFilter) []model.State {
	matching := []model.State{}
	for _, state := range states {
		if filter.MatchesState(state) {
			matching = append(matching, state)
		}
	}
	slices.SortFunc(matching, model.CompareStates)
	return matching
}

// filterDivergences returns the divergences that filter selects, ordered by source.
func filterDivergences(divergences []model.Divergence, filter model.DivergenceFilter) []model.Divergence {
	matching := []model.Divergence{}
	for _, divergence := range divergences {
		if filter.Matches(divergence) {
			matching = append(matching, divergence)
		}
	}
	slices.SortFunc(matching, model.CompareDivergences)
	return matching
}

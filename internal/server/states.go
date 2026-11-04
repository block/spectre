package server

import (
	"cmp"
	"slices"

	"github.com/block/spectre/internal/model"
)

// filterStates returns the states that filter selects, ordered by service and
// environment.
func filterStates(states []model.State, filter model.StateFilter) []model.State {
	matching := []model.State{}
	for _, state := range states {
		if filter.Matches(state) {
			matching = append(matching, state)
		}
	}
	slices.SortFunc(matching, func(a, b model.State) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.Environment, b.Environment))
	})
	return matching
}

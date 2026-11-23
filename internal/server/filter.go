package server

import (
	"cmp"
	"slices"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/storage"
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
	slices.SortFunc(matching, func(a, b model.State) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.Environment, b.Environment))
	})
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
	slices.SortFunc(matching, func(a, b model.Divergence) int {
		return cmp.Or(
			cmp.Compare(a.Service, b.Service),
			cmp.Compare(a.Environment, b.Environment),
			// Reversed, so ingress lists before egress.
			cmp.Compare(b.Direction, a.Direction),
		)
	})
	return matching
}

// countedCalls returns states counting only the calls their tallies hold, and
// the total of each tally.
func countedCalls(states []model.State, tallies []storage.Tally) ([]model.State, []model.CallTotal) {
	byKey := make(map[[2]string]storage.Tally, len(tallies))
	totals := make([]model.CallTotal, 0, len(tallies))
	for _, tally := range tallies {
		byKey[[2]string{tally.Service, tally.Environment}] = tally
		totals = append(totals, tally.Total())
	}
	counted := make([]model.State, 0, len(states))
	for _, state := range states {
		if tally, ok := byKey[[2]string{state.Service, state.Environment}]; ok {
			state.Calls = tally.RunCalls(state.Run)
		}
		counted = append(counted, state)
	}
	return counted, totals
}

// filterTotals returns the call totals that filter selects, ordered by service
// and environment. A revision or node filter selects only listed mirrors' totals.
func filterTotals(listing model.Listing, filter model.DivergenceFilter) []model.CallTotal {
	listed := map[[2]string]bool{}
	for _, state := range listing.States {
		listed[[2]string{state.Service, state.Environment}] = true
	}
	for _, divergence := range listing.Divergences {
		listed[[2]string{divergence.Service, divergence.Environment}] = true
	}
	_, byRevision := filter.Revision.Get()
	_, byNode := filter.Node.Get()
	matching := []model.CallTotal{}
	for _, total := range listing.CallTotals {
		// A total spans revisions and quarantines, so only these filters apply.
		if !filter.MatchesServiceEnvironment(total.Service, total.Environment) {
			continue
		}
		if (byRevision || byNode) && !listed[[2]string{total.Service, total.Environment}] {
			continue
		}
		matching = append(matching, total)
	}
	slices.SortFunc(matching, func(a, b model.CallTotal) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.Environment, b.Environment))
	})
	return matching
}

package server

import (
	"cmp"
	"slices"

	"github.com/block/spectre/internal/model"
)

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

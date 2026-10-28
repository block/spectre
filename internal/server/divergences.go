package server

import (
	"cmp"
	"slices"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	pb "github.com/block/spectre/spectrepb"
)

// validateDivergence checks that a reported divergence identifies its source
// and quarantine, so every store can rely on them.
func validateDivergence(divergence *pb.Divergence) error {
	if divergence.GetService() == "" || divergence.GetEnvironment() == "" || divergence.GetRevision() == "" {
		return errors.New("divergence needs a service, environment, and revision")
	}
	if _, err := DirectionFromMessage(divergence.GetDirection()); err != nil {
		return err
	}
	quarantine := divergence.GetQuarantine()
	if quarantine.GetSince() == nil {
		return errors.New("divergence needs a quarantine with its start time")
	}
	if _, err := ReasonFromMessage(quarantine); err != nil {
		return errors.Wrap(err, "quarantine")
	}
	return nil
}

// filterDivergences returns the divergences that match the request, ordered by source.
func filterDivergences(divergences []*pb.Divergence, request *pb.ListDivergencesRequest) []*pb.Divergence {
	matching := []*pb.Divergence{}
	for _, divergence := range divergences {
		quarantine := divergence.GetQuarantine()
		// Getters hide whether an optional field is set, so presence is a nil check.
		if matches(From(request.GetService(), request.Service != nil), Some(divergence.GetService())) &&
			matches(From(request.GetEnvironment(), request.Environment != nil), Some(divergence.GetEnvironment())) &&
			matches(From(request.GetRevision(), request.Revision != nil), Some(divergence.GetRevision())) &&
			matches(From(request.GetNode(), request.Node != nil), From(quarantine.GetNode(), quarantine.Node != nil)) {
			matching = append(matching, divergence)
		}
	}
	slices.SortFunc(matching, func(a, b *pb.Divergence) int {
		return cmp.Or(
			cmp.Compare(a.GetService(), b.GetService()),
			cmp.Compare(a.GetEnvironment(), b.GetEnvironment()),
			cmp.Compare(a.GetDirection(), b.GetDirection()),
		)
	})
	return matching
}

// matches reports whether value passes a filter. An unset filter matches
// every value, including an absent one.
func matches(filter, value Option[string]) bool {
	expected, ok := filter.Get()
	return !ok || value == Some(expected)
}

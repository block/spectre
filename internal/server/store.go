package server

import (
	"cmp"
	"slices"
	"sync"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	pb "github.com/block/spectre/spectrepb"
)

// store holds the latest quarantine of each mirror direction. Stored messages
// are never mutated, so they may be shared.
type store struct {
	mu          sync.Mutex
	divergences map[sourceKey]*pb.Divergence
}

func newStore() *store {
	return &store{divergences: map[sourceKey]*pb.Divergence{}}
}

// sourceKey identifies the proxy of one direction of a service environment's mirror.
type sourceKey struct {
	service, environment string
	direction            pb.Direction
}

// record keeps a divergence unless its source has already reported a later one.
func (s *store) record(divergence *pb.Divergence) error {
	if divergence.GetService() == "" || divergence.GetEnvironment() == "" || divergence.GetRevision() == "" {
		return errors.New("divergence needs a service, environment, and revision")
	}
	if _, err := directionFromMessage(divergence.GetDirection()); err != nil {
		return err
	}
	quarantine := divergence.GetQuarantine()
	if quarantine.GetSince() == nil {
		return errors.New("divergence needs a quarantine with its start time")
	}
	if _, err := reasonFromMessage(quarantine); err != nil {
		return errors.Wrap(err, "quarantine")
	}
	key := sourceKey{service: divergence.GetService(), environment: divergence.GetEnvironment(), direction: divergence.GetDirection()}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A retry from a mirror that has since restarted and quarantined again must not
	// replace the newer quarantine.
	if existing, ok := s.divergences[key]; ok && existing.GetQuarantine().GetSince().AsTime().After(quarantine.GetSince().AsTime()) {
		return nil
	}
	s.divergences[key] = divergence
	return nil
}

// clear removes the divergences of both directions of a service environment's mirror.
func (s *store) clear(service, environment string) error {
	if service == "" || environment == "" {
		return errors.New("clearing divergences needs a service and environment")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.divergences {
		if key.service == service && key.environment == environment {
			delete(s.divergences, key)
		}
	}
	return nil
}

// listDivergences returns the matching divergences, ordered by source.
func (s *store) listDivergences(request *pb.ListDivergencesRequest) []*pb.Divergence {
	s.mu.Lock()
	defer s.mu.Unlock()
	divergences := []*pb.Divergence{}
	for _, divergence := range s.divergences {
		quarantine := divergence.GetQuarantine()
		// Getters hide whether an optional field is set, so presence is a nil check.
		if matches(From(request.GetService(), request.Service != nil), Some(divergence.GetService())) &&
			matches(From(request.GetEnvironment(), request.Environment != nil), Some(divergence.GetEnvironment())) &&
			matches(From(request.GetRevision(), request.Revision != nil), Some(divergence.GetRevision())) &&
			matches(From(request.GetNode(), request.Node != nil), From(quarantine.GetNode(), quarantine.Node != nil)) {
			divergences = append(divergences, divergence)
		}
	}
	slices.SortFunc(divergences, func(a, b *pb.Divergence) int {
		return cmp.Or(
			cmp.Compare(a.GetService(), b.GetService()),
			cmp.Compare(a.GetEnvironment(), b.GetEnvironment()),
			cmp.Compare(a.GetDirection(), b.GetDirection()),
		)
	})
	return divergences
}

// matches reports whether value passes a filter. An unset filter matches
// every value, including an absent one.
func matches(filter, value Option[string]) bool {
	expected, ok := filter.Get()
	return !ok || value == Some(expected)
}

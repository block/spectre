package coordinator

import (
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/block/spectre/internal/coordinator/pb"
)

// store holds every quarantine any mirror has reported. Stored messages are
// never mutated, so they may be shared.
type store struct {
	mu          sync.Mutex
	divergences []*divergenceRecord
	groups      map[divergenceKey]*divergenceRecord
}

func newStore() *store {
	return &store{groups: map[divergenceKey]*divergenceRecord{}}
}

// divergenceKey identifies identical quarantines. Detail is left out, because it
// may name things that differ on every occurrence, such as request IDs.
type divergenceKey struct {
	service, environment, revision string
	kind                           string
	direction, protocol, route     string
	node                           string
	// differences joins the difference paths, because a key cannot hold a slice.
	differences string
}

type divergenceRecord struct {
	key       divergenceKey
	latest    *pb.Quarantine
	firstSeen time.Time
	instances map[string]struct{}
}

// report records a mirror's quarantines.
func (s *store) report(mirror *pb.Mirror) error {
	if mirror.GetInstance() == "" || mirror.GetService() == "" || mirror.GetEnvironment() == "" {
		return errors.New("mirror needs an instance, service, and environment")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, source := range mirror.GetSources() {
		if quarantine := source.GetQuarantine(); quarantine != nil {
			s.recordQuarantine(mirror, quarantine)
		}
	}
	return nil
}

// recordQuarantine groups a quarantine with identical ones. Callers hold s.mu.
func (s *store) recordQuarantine(mirror *pb.Mirror, quarantine *pb.Quarantine) {
	key := divergenceKey{
		service:     mirror.GetService(),
		environment: mirror.GetEnvironment(),
		revision:    mirror.GetRevision(),
		kind:        quarantine.GetKind(),
		direction:   quarantine.GetTraffic().GetDirection(),
		protocol:    quarantine.GetTraffic().GetProtocol(),
		route:       quarantine.GetTraffic().GetRoute(),
		node:        quarantine.GetNode(),
		differences: strings.Join(quarantine.GetDifferences(), "\x00"),
	}
	since := quarantine.GetSince().AsTime()
	record, ok := s.groups[key]
	if !ok {
		record = &divergenceRecord{key: key, latest: quarantine, firstSeen: since, instances: map[string]struct{}{}}
		s.groups[key] = record
		s.divergences = append(s.divergences, record)
	}
	record.instances[mirror.GetInstance()] = struct{}{}
	if since.After(record.latest.GetSince().AsTime()) {
		record.latest = quarantine
	}
	if since.Before(record.firstSeen) {
		record.firstSeen = since
	}
}

// listDivergences returns the matching divergences in the order first reported.
func (s *store) listDivergences(request *pb.ListDivergencesRequest) []*pb.Divergence {
	s.mu.Lock()
	defer s.mu.Unlock()
	divergences := []*pb.Divergence{}
	for _, record := range s.divergences {
		key := record.key
		if !matches(request.GetService(), key.service) || !matches(request.GetEnvironment(), key.environment) ||
			!matches(request.GetRevision(), key.revision) || !matches(request.GetNode(), key.node) {
			continue
		}
		divergences = append(divergences, &pb.Divergence{
			Service:     key.service,
			Environment: key.environment,
			Revision:    key.revision,
			Latest:      record.latest,
			Mirrors:     int64(len(record.instances)),
			FirstSeen:   timestamppb.New(record.firstSeen),
		})
	}
	return divergences
}

// matches reports whether value passes a filter, which is unset when empty.
func matches(filter, value string) bool {
	return filter == "" || filter == value
}

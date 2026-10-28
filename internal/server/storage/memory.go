package storage

import (
	"context"
	"sync"

	pb "github.com/block/spectre/spectrepb"
)

// Memory keeps divergences in memory, so a restart loses them. Stored messages
// are never mutated, so they may be shared.
type Memory struct {
	mu          sync.Mutex
	divergences map[sourceKey]*pb.Divergence
}

var _ Store = (*Memory)(nil)

// sourceKey identifies the proxy of one direction of a service environment's mirror.
type sourceKey struct {
	service, environment string
	direction            pb.Direction
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{divergences: map[sourceKey]*pb.Divergence{}}
}

// Record keeps a divergence unless its source has already reported a later quarantine.
func (m *Memory) Record(_ context.Context, divergence *pb.Divergence) error {
	key := sourceKey{service: divergence.GetService(), environment: divergence.GetEnvironment(), direction: divergence.GetDirection()}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.divergences[key]; ok && quarantinedAfter(existing, divergence) {
		return nil
	}
	m.divergences[key] = divergence
	return nil
}

// Clear removes the divergences of both directions of a service environment's mirror.
func (m *Memory) Clear(_ context.Context, service, environment string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.divergences {
		if key.service == service && key.environment == environment {
			delete(m.divergences, key)
		}
	}
	return nil
}

// List returns every stored divergence, in no particular order.
func (m *Memory) List(_ context.Context) ([]*pb.Divergence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	divergences := make([]*pb.Divergence, 0, len(m.divergences))
	for _, divergence := range m.divergences {
		divergences = append(divergences, divergence)
	}
	return divergences, nil
}

// Close does nothing, because memory holds no resources.
func (m *Memory) Close() error { return nil }

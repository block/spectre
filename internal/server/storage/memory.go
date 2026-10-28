package storage

import (
	"context"
	"sync"

	"github.com/block/spectre/internal/model"
)

// Memory keeps divergences in memory, so a restart loses them. Stored
// divergences share their slices with callers, who never mutate them.
type Memory struct {
	mu          sync.Mutex
	divergences map[sourceKey]model.Divergence
}

var _ Store = (*Memory)(nil)

// sourceKey identifies the proxy of one direction of a service environment's mirror.
type sourceKey struct {
	service, environment string
	direction            model.Direction
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{divergences: map[sourceKey]model.Divergence{}}
}

// Record keeps a divergence unless its source has already reported a later quarantine.
func (m *Memory) Record(_ context.Context, divergence model.Divergence) error {
	key := sourceKey{service: divergence.Service, environment: divergence.Environment, direction: divergence.Direction}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.divergences[key]; ok && quarantinedAfter(existing.Quarantine, divergence.Quarantine) {
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
func (m *Memory) List(_ context.Context) ([]model.Divergence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	divergences := make([]model.Divergence, 0, len(m.divergences))
	for _, divergence := range m.divergences {
		divergences = append(divergences, divergence)
	}
	return divergences, nil
}

// Close does nothing, because memory holds no resources.
func (m *Memory) Close() error { return nil }

package storage

import (
	"context"
	"net/url"
	"sync"
	"time"

	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
)

func init() {
	register("memory", func(context.Context, *url.URL) (Store, error) { return NewMemory(), nil })
}

// Memory keeps states in memory, so a restart loses them. Stored states share
// their maps with callers, who never mutate them.
type Memory struct {
	mu          sync.Mutex
	states      map[string]received
	divergences map[sourceKey]model.Divergence
	// cleared maps each stateKey to when its divergences were last cleared.
	cleared map[string]time.Time
}

// sourceKey identifies the proxy of one direction of a service environment's mirror.
type sourceKey struct {
	service, environment string
	direction            model.Direction
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{states: map[string]received{}, divergences: map[sourceKey]model.Divergence{}, cleared: map[string]time.Time{}}
}

// Put replaces the state of a service environment's mirror.
func (m *Memory) Put(_ context.Context, state model.State, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[stateKey(state.Service, state.Environment)] = received{State: state, Received: at}
	return nil
}

// States returns the states received at or after since, in no particular
// order, and forgets older ones.
func (m *Memory) States(_ context.Context, since time.Time) ([]model.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	states := make([]model.State, 0, len(m.states))
	for key, stored := range m.states {
		if stored.Received.Before(since) {
			delete(m.states, key)
			continue
		}
		states = append(states, stored.State)
	}
	return states, nil
}

// Record keeps a divergence unless its source has already reported a later
// quarantine, or its service environment was cleared after it began.
func (m *Memory) Record(_ context.Context, divergence model.Divergence) error {
	key := sourceKey{service: divergence.Service, environment: divergence.Environment, direction: divergence.Direction}
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.divergences[key]
	cleared, wasCleared := m.cleared[stateKey(divergence.Service, divergence.Environment)]
	if ignored(From(stored.Quarantine, ok), From(cleared, wasCleared), divergence.Quarantine) {
		return nil
	}
	m.divergences[key] = divergence
	return nil
}

// Clear removes the divergences of a service environment's mirror, and
// ignores its quarantines that began before at.
func (m *Memory) Clear(_ context.Context, service, environment string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.divergences {
		if key.service == service && key.environment == environment {
			delete(m.divergences, key)
		}
	}
	m.cleared[stateKey(service, environment)] = at
	return nil
}

// Divergences returns every stored divergence, in no particular order.
func (m *Memory) Divergences(_ context.Context) ([]model.Divergence, error) {
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

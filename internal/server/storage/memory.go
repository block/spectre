package storage

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/block/spectre/internal/model"
)

func init() {
	register("memory", func(context.Context, *url.URL) (Store, error) { return NewMemory(), nil })
}

// Memory keeps service environments in memory, so a restart loses them.
// Stored values share their maps with callers, who never mutate them.
type Memory struct {
	mu           sync.Mutex
	environments map[string]model.ServiceEnvironment
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{environments: map[string]model.ServiceEnvironment{}}
}

// Observe replaces a mirror's state and optionally records its quarantines.
func (m *Memory) Observe(_ context.Context, state model.State, at time.Time, recordQuarantines bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(state.Service, state.Environment)
	m.environments[key] = m.environments[key].Observed(state, at, recordQuarantines)
	return nil
}

// List returns live states and recorded divergences, forgetting expired states.
func (m *Memory) List(_ context.Context, since time.Time) (model.Listing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	listing := model.Listing{States: []model.State{}, Divergences: []model.Divergence{}}
	for key, environment := range m.environments {
		environment = environment.ForgetStateBefore(since)
		m.environments[key] = environment
		state, reporting := environment.State.Get()
		if reporting {
			listing.States = append(listing.States, state)
		} else if len(environment.Divergences) == 0 && !environment.Cleared.Ok() {
			delete(m.environments, key)
		}
		listing.Divergences = append(listing.Divergences, environment.Divergences...)
	}
	return listing, nil
}

// Clear removes recorded divergences and ignores quarantines from at or before.
func (m *Memory) Clear(_ context.Context, service, environment string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(service, environment)
	stored := m.environments[key]
	stored.Service, stored.Environment = service, environment
	m.environments[key] = stored.ClearedAt(at)
	return nil
}

// Close does nothing, because memory holds no resources.
func (m *Memory) Close() error { return nil }

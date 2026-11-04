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

// Memory keeps states in memory, so a restart loses them. Stored states share
// their maps with callers, who never mutate them.
type Memory struct {
	mu     sync.Mutex
	states map[string]received
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{states: map[string]received{}}
}

// Put replaces the state of a service environment's mirror.
func (m *Memory) Put(_ context.Context, state model.State, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[stateKey(state.Service, state.Environment)] = received{State: state, Received: at}
	return nil
}

// List returns the states received at or after since, in no particular
// order, and forgets older ones.
func (m *Memory) List(_ context.Context, since time.Time) ([]model.State, error) {
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

// Close does nothing, because memory holds no resources.
func (m *Memory) Close() error { return nil }

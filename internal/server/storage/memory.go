package storage

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"sync"

	. "github.com/alecthomas/types/optional"
	"github.com/block/spectre/internal/model"
)

func init() {
	register("memory", func(context.Context, *url.URL) (Store, error) { return NewMemory(), nil })
}

// Memory persists immutable aggregates under a mutex. A restart loses them.
type Memory struct {
	mu           sync.Mutex
	environments map[string]model.ServiceEnvironment
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{environments: map[string]model.ServiceEnvironment{}} }

// Update atomically changes or deletes one environment.
func (m *Memory) Update(_ context.Context, service, environment string, change func(current model.ServiceEnvironment) Option[model.ServiceEnvironment]) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(service, environment)
	current, found := m.environments[key]
	if !found {
		current = newEnvironment(service, environment)
	}
	if next, ok := change(current).Get(); ok {
		m.environments[key] = next
	} else {
		delete(m.environments, key)
	}
	return nil
}

// List returns immutable snapshots in no particular order.
func (m *Memory) List(_ context.Context) ([]model.ServiceEnvironment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Collect(maps.Values(m.environments)), nil
}

// Close does nothing because memory holds no resources.
func (m *Memory) Close() error { return nil }

// Package storage keeps the divergences that mirrors report to the SPECTRE
// server. A URL selects the backend by its scheme, such as memory://, redis://,
// or redis-cluster://.
package storage

import (
	"context"
	"net/url"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/model"
)

// Constructor opens a store at a URL whose scheme it is registered for.
type Constructor func(ctx context.Context, storageURL *url.URL) (Store, error)

// constructors maps each URL scheme to its backend. Backends write it only from
// init, so reading it needs no lock.
var constructors = map[string]Constructor{} //nolint:gochecknoglobals // Backends register themselves.

// register makes Open use constructor for URLs with scheme. Call it only from init.
func register(scheme string, constructor Constructor) {
	constructors[scheme] = constructor
}

// Config selects where the SPECTRE server keeps divergences.
type Config struct {
	Storage *url.URL `default:"memory://" placeholder:"URL" help:"Where to keep divergences: memory://, which a restart loses, a Redis URL such as redis://localhost:6379/0, or a Redis Cluster URL such as redis-cluster://localhost:7000?addr=localhost:7001."`
}

// Validate checks that a backend serves the storage URL's scheme.
func (c Config) Validate() error {
	_, err := c.constructor()
	return err
}

func (c Config) constructor() (Constructor, error) {
	if c.Storage == nil {
		return nil, errors.New("storage URL is required")
	}
	constructor, ok := constructors[c.Storage.Scheme]
	if !ok {
		return nil, errors.Errorf("unknown storage scheme %q", c.Storage.Scheme)
	}
	return constructor, nil
}

// Store keeps the latest divergence of each mirror direction. Callers validate
// divergences before recording them and never mutate stored or listed ones.
type Store interface {
	// Record keeps a divergence unless its source has already reported a later quarantine.
	Record(ctx context.Context, divergence model.Divergence) error
	// Clear removes the divergences of both directions of a service environment's mirror.
	Clear(ctx context.Context, service, environment string) error
	// List returns every stored divergence, in no particular order.
	List(ctx context.Context) ([]model.Divergence, error)
	Close() error
}

// Open returns the store the configuration's URL selects.
func Open(ctx context.Context, config Config) (Store, error) {
	constructor, err := config.constructor()
	if err != nil {
		return nil, err
	}
	return constructor(ctx, config.Storage)
}

// quarantinedAfter reports whether existing began after incoming. A retry from a
// mirror that has since restarted and quarantined again must not replace it.
func quarantinedAfter(existing, incoming model.Quarantine) bool {
	return existing.Since.After(incoming.Since)
}

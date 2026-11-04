// Package storage keeps the states that mirrors report to the SPECTRE
// server. A URL selects the backend by its scheme, such as memory://, redis://,
// or redis-cluster://.
package storage

import (
	"context"
	"net/url"
	"strconv"
	"time"

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

// Config selects where the SPECTRE server keeps mirror states.
type Config struct {
	Storage *url.URL `default:"memory://" placeholder:"URL" help:"Where to keep mirror states: memory://, which a restart loses, a Redis URL such as redis://localhost:6379/0, or a Redis Cluster URL such as redis-cluster://localhost:7000?addr=localhost:7001."`
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

// Store keeps the latest state of each service environment's mirror, with when
// the server received it. Callers validate states and never mutate stored ones.
type Store interface {
	// Put replaces the state of a service environment's mirror.
	Put(ctx context.Context, state model.State, received time.Time) error
	// List returns the states received at or after since, in no particular
	// order, and forgets older ones.
	List(ctx context.Context, since time.Time) ([]model.State, error)
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

// stateKey identifies the mirror of a service environment. Quoting keeps the
// parts unambiguous whatever characters they hold.
func stateKey(service, environment string) string {
	return strconv.Quote(service) + " " + strconv.Quote(environment)
}

// received is a state as stored, with when the server received it.
type received struct {
	State    model.State `json:"state"`
	Received time.Time   `json:"received"`
}

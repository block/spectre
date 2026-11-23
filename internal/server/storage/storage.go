// Package storage keeps the states that mirrors report to the SPECTRE
// server. A URL selects the backend by its scheme, such as memory://, redis://,
// or redis-cluster://.
package storage

import (
	"context"
	"net/url"
	"strconv"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

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

// Store persists immutable service-environment snapshots.
// Update callbacks may run more than once and must not mutate shared values.
type Store interface {
	// Update atomically changes one aggregate, creating it if absent or deleting it for None.
	Update(ctx context.Context, service, environment string, change func(current model.ServiceEnvironment) Option[model.ServiceEnvironment]) error
	// List returns snapshots in no particular order.
	List(ctx context.Context) ([]model.ServiceEnvironment, error)
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

// newEnvironment supplies the identity of an aggregate that is not stored yet.
func newEnvironment(service, environment string) model.ServiceEnvironment {
	return model.ServiceEnvironment{EnvironmentStatus: model.EnvironmentStatus{Service: service, Environment: environment}}
}

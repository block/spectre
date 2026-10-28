// Package storage keeps the divergences that mirrors report to the SPECTRE
// server, either in memory or in Redis.
package storage

import (
	"context"

	"github.com/alecthomas/errors"
	"github.com/redis/go-redis/v9"

	pb "github.com/block/spectre/spectrepb"
)

// Backend names where the SPECTRE server keeps divergences.
type Backend string

// Backends the SPECTRE server can keep divergences in.
const (
	BackendMemory Backend = "memory"
	BackendRedis  Backend = "redis"
)

// Config selects where the SPECTRE server keeps divergences.
type Config struct {
	Storage  Backend `default:"memory" enum:"memory,redis" help:"Where to keep divergences: memory, which a restart loses, or redis."`
	RedisURL string  `name:"redis-url" placeholder:"URL" help:"URL of the Redis server that keeps divergences when storage is redis, such as redis://localhost:6379/0."`
}

// Validate checks that the selected backend is configured.
func (c Config) Validate() error {
	switch c.Storage {
	case BackendMemory:
		return nil
	case BackendRedis:
		if c.RedisURL == "" {
			return errors.New("redis storage needs a Redis URL")
		}
		if _, err := redis.ParseURL(c.RedisURL); err != nil {
			return errors.Wrap(err, "redis URL")
		}
		return nil
	}
	return errors.Errorf("unknown storage %q", c.Storage)
}

// Store keeps the latest divergence of each mirror direction. Callers validate
// divergences before recording them and never mutate stored or listed ones.
type Store interface {
	// Record keeps a divergence unless its source has already reported a later quarantine.
	Record(ctx context.Context, divergence *pb.Divergence) error
	// Clear removes the divergences of both directions of a service environment's mirror.
	Clear(ctx context.Context, service, environment string) error
	// List returns every stored divergence, in no particular order.
	List(ctx context.Context) ([]*pb.Divergence, error)
	Close() error
}

// Open returns the store the configuration selects.
func Open(ctx context.Context, config Config) (Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	switch config.Storage {
	case BackendMemory:
		return NewMemory(), nil
	case BackendRedis:
		return NewRedis(ctx, config)
	}
	return nil, errors.Errorf("unknown storage %q", config.Storage)
}

// quarantinedAfter reports whether existing began after incoming. A retry from a
// mirror that has since restarted and quarantined again must not replace it.
func quarantinedAfter(existing, incoming *pb.Divergence) bool {
	return existing.GetQuarantine().GetSince().AsTime().After(incoming.GetQuarantine().GetSince().AsTime())
}

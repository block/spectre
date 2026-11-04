package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/alecthomas/errors"
	"github.com/redis/go-redis/v9"

	"github.com/block/spectre/internal/model"
)

func init() {
	for _, scheme := range []string{"redis", "rediss", "redis" + clusterSuffix, "rediss" + clusterSuffix} {
		register(scheme, func(ctx context.Context, storageURL *url.URL) (Store, error) {
			// Returning a nil *Redis directly would make a non-nil Store.
			store, err := NewRedis(ctx, storageURL)
			if err != nil {
				return nil, err
			}
			return store, nil
		})
	}
}

// clusterSuffix marks a scheme that connects to a Redis Cluster. go-redis parses
// cluster URLs with the plain schemes, so the suffix is the only way to tell.
const clusterSuffix = "-cluster"

// statesKey is the Redis hash that maps each stateKey to its stored state as JSON.
const statesKey = "spectre:states"

// forgetIfUnchanged deletes a hash field only if it still holds the given value,
// so forgetting a stale state never deletes a newer one.
const forgetIfUnchanged = `
if redis.call("HGET", KEYS[1], ARGV[1]) == ARGV[2] then
  return redis.call("HDEL", KEYS[1], ARGV[1])
end
return 0
`

// Redis keeps states in one hash, shared by servers using the same database.
// Commands touch only that key, so in a cluster one node's slot serves them all.
type Redis struct {
	client redis.UniversalClient
	forget *redis.Script
}

var _ Store = (*Redis)(nil)

// NewRedis connects to the Redis server at a redis:// or rediss:// URL, or to
// the Redis Cluster at a redis-cluster:// or rediss-cluster:// URL.
func NewRedis(ctx context.Context, storageURL *url.URL) (*Redis, error) {
	client, err := newRedisClient(storageURL)
	if err != nil {
		return nil, errors.Wrap(err, "redis URL")
	}
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close() //nolint:errcheck // The connection failed, so closing has nothing to report.
		return nil, errors.Wrap(err, "connect to Redis")
	}
	return &Redis{client: client, forget: redis.NewScript(forgetIfUnchanged)}, nil
}

func newRedisClient(storageURL *url.URL) (redis.UniversalClient, error) {
	scheme, cluster := strings.CutSuffix(storageURL.Scheme, clusterSuffix)
	goRedisURL := *storageURL
	goRedisURL.Scheme = scheme
	if !cluster {
		options, err := redis.ParseURL(goRedisURL.String())
		if err != nil {
			return nil, errors.WithStack(err)
		}
		return redis.NewClient(options), nil
	}
	// go-redis ignores the path of a cluster URL, which would otherwise silently
	// store states in database 0 instead of the one the path names.
	if strings.Trim(goRedisURL.Path, "/") != "" {
		return nil, errors.Errorf("a Redis Cluster has no databases to select with path %q", goRedisURL.Path)
	}
	options, err := redis.ParseClusterURL(goRedisURL.String())
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return redis.NewClusterClient(options), nil
}

// Put replaces the state of a service environment's mirror.
func (r *Redis) Put(ctx context.Context, state model.State, at time.Time) error {
	value, err := json.Marshal(received{State: state, Received: at})
	if err != nil {
		return errors.Wrap(err, "encode state")
	}
	err = r.client.HSet(ctx, statesKey, stateKey(state.Service, state.Environment), value).Err()
	return errors.Wrap(err, "store state")
}

// List returns the states received at or after since, in no particular
// order, and forgets older ones.
func (r *Redis) List(ctx context.Context, since time.Time) ([]model.State, error) {
	values, err := r.client.HGetAll(ctx, statesKey).Result()
	if err != nil {
		return nil, errors.Wrap(err, "list states")
	}
	states := make([]model.State, 0, len(values))
	for field, value := range values {
		stored, err := decodeState([]byte(value))
		if err != nil {
			return nil, err
		}
		if !stored.Received.Before(since) {
			states = append(states, stored.State)
			continue
		}
		if err := r.forget.Run(ctx, r.client, []string{statesKey}, field, value).Err(); err != nil {
			return nil, errors.Wrap(err, "forget stale state")
		}
	}
	return states, nil
}

// Close disconnects from Redis.
func (r *Redis) Close() error {
	return errors.Wrap(r.client.Close(), "close Redis client")
}

// decodeState rejects unknown fields, so a stored format that has drifted
// fails loudly instead of losing data.
func decodeState(data []byte) (received, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var stored received
	if err := decoder.Decode(&stored); err != nil {
		return received{}, errors.Wrap(err, "decode state")
	}
	return stored, nil
}

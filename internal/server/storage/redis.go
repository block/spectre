package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
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

// divergencesKey is the Redis hash that maps each sourceField to its divergence
// as JSON, and each clearedField to when its divergences were last cleared.
const divergencesKey = "spectre:divergences"

// clearedSuffix ends every clearedField. A quoted environment ends with a
// quote, so no sourceField ends with it.
const clearedSuffix = `" cleared`

// forgetIfUnchanged deletes a hash field only if it still holds the given value,
// so forgetting a stale state never deletes a newer one.
const forgetIfUnchanged = `
if redis.call("HGET", KEYS[1], ARGV[1]) == ARGV[2] then
  return redis.call("HDEL", KEYS[1], ARGV[1])
end
return 0
`

// Redis keeps states and divergences in two hashes, shared by servers using the
// same database. Each command touches one key, so it works in a cluster.
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

// States returns the states received at or after since, in no particular
// order, and forgets older ones.
func (r *Redis) States(ctx context.Context, since time.Time) ([]model.State, error) {
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

// Record keeps a divergence unless its source has already reported a later
// quarantine, or its service environment was cleared after it began.
func (r *Redis) Record(ctx context.Context, divergence model.Divergence) error {
	field := sourceField(divergence.Service, divergence.Environment, divergence.Direction)
	cleared := clearedField(divergence.Service, divergence.Environment)
	value, err := json.Marshal(divergence)
	if err != nil {
		return errors.Wrap(err, "encode divergence")
	}
	// The comparison happens in Go, so watch the hash and retry whenever another
	// write lands between reading the stored values and replacing the divergence.
	for {
		err := r.client.Watch(ctx, func(tx *redis.Tx) error {
			values, err := tx.HMGet(ctx, divergencesKey, field, cleared).Result()
			if err != nil {
				return errors.Wrap(err, "read divergence")
			}
			stored, err := decodeOptional(values[0], func(data []byte) (model.Quarantine, error) {
				existing, err := decodeDivergence(data)
				return existing.Quarantine, err
			})
			if err != nil {
				return err
			}
			clearedAt, err := decodeOptional(values[1], decodeCleared)
			if err != nil {
				return err
			}
			if ignored(stored, clearedAt, divergence.Quarantine) {
				return nil
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				return errors.WithStack(pipe.HSet(ctx, divergencesKey, field, value).Err())
			})
			return errors.WithStack(err)
		}, divergencesKey)
		if !errors.Is(err, redis.TxFailedErr) {
			return errors.Wrap(err, "record divergence")
		}
	}
}

// Clear removes the divergences of a service environment's mirror, and
// ignores its quarantines that began before at.
func (r *Redis) Clear(ctx context.Context, service, environment string, at time.Time) error {
	value, err := json.Marshal(at)
	if err != nil {
		return errors.Wrap(err, "encode clear time")
	}
	_, err = r.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HDel(ctx, divergencesKey, sourceField(service, environment, model.Ingress), sourceField(service, environment, model.Egress))
		pipe.HSet(ctx, divergencesKey, clearedField(service, environment), value)
		return nil
	})
	return errors.Wrap(err, "clear divergences")
}

// Divergences returns every stored divergence, in no particular order.
func (r *Redis) Divergences(ctx context.Context) ([]model.Divergence, error) {
	values, err := r.client.HGetAll(ctx, divergencesKey).Result()
	if err != nil {
		return nil, errors.Wrap(err, "list divergences")
	}
	divergences := make([]model.Divergence, 0, len(values))
	for field, value := range values {
		if strings.HasSuffix(field, clearedSuffix) {
			continue
		}
		divergence, err := decodeDivergence([]byte(value))
		if err != nil {
			return nil, err
		}
		divergences = append(divergences, divergence)
	}
	return divergences, nil
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

// sourceField identifies the proxy of one direction of a service environment's mirror.
func sourceField(service, environment string, direction model.Direction) string {
	return stateKey(service, environment) + " " + string(direction)
}

// clearedField holds when the divergences of a service environment were last cleared.
func clearedField(service, environment string) string {
	return stateKey(service, environment) + " cleared"
}

// decodeOptional decodes a value HMGET returned, which is nil for a missing field.
func decodeOptional[T any](value any, decode func(data []byte) (T, error)) (Option[T], error) {
	text, ok := value.(string)
	if !ok {
		return None[T](), nil
	}
	decoded, err := decode([]byte(text))
	if err != nil {
		return None[T](), err
	}
	return Some(decoded), nil
}

// decodeDivergence rejects unknown fields, so a stored format that has drifted
// fails loudly instead of losing data.
func decodeDivergence(data []byte) (model.Divergence, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var divergence model.Divergence
	if err := decoder.Decode(&divergence); err != nil {
		return model.Divergence{}, errors.Wrap(err, "decode divergence")
	}
	return divergence, nil
}

func decodeCleared(data []byte) (time.Time, error) {
	var at time.Time
	err := json.Unmarshal(data, &at)
	return at, errors.Wrap(err, "decode clear time")
}

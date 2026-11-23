package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"

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

// environmentsKey stores one JSON aggregate per service environment.
const environmentsKey = "spectre:environments"

// replaceIfUnchanged compares one field, so unrelated environments do not conflict.
const replaceIfUnchanged = `
local current=redis.call("HGET",KEYS[1],ARGV[1]) or ""
if current~=ARGV[2] then return 0 end
if ARGV[3]=="" then
 redis.call("HDEL",KEYS[1],ARGV[1])
else
 redis.call("HSET",KEYS[1],ARGV[1],ARGV[3])
end
return 1
`

// Redis stores immutable service-environment aggregates in one cluster-compatible hash.
type Redis struct {
	client redis.UniversalClient
}

var _ Store = (*Redis)(nil)

type replaceCommand struct{ *redis.Cmd }

// NoRetry prevents reapplying a transition if Redis commits it but loses the reply.
func (replaceCommand) NoRetry() bool { return true }

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
	for _, key := range []string{"spectre:states", "spectre:divergences", "spectre:calls:{spectre:divergences}"} {
		exists, err := client.Exists(ctx, key).Result()
		if err != nil || exists > 0 {
			_ = client.Close() //nolint:errcheck // Opening failed; close the unused client.
			if err != nil {
				return nil, errors.Wrap(err, "check Redis storage format")
			}
			return nil, errors.Errorf("Redis contains previous-format SPECTRE data in %s; keep the previous server or use a fresh database", key)
		}
	}
	return &Redis{client: client}, nil
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

// Update retries a pure transition if another writer changes the same aggregate.
func (r *Redis) Update(ctx context.Context, service, environment string, change func(current model.ServiceEnvironment) Option[model.ServiceEnvironment]) error {
	field := stateKey(service, environment)
	for {
		value, err := r.client.HGet(ctx, environmentsKey, field).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return errors.Wrap(err, "read service environment")
		}
		current := newEnvironment(service, environment)
		if err == nil {
			current, err = decodeEnvironment([]byte(value))
			if err != nil {
				return err
			}
		}
		replacement := ""
		if next, ok := change(current).Get(); ok {
			data, err := json.Marshal(next)
			if err != nil {
				return errors.Wrap(err, "encode service environment")
			}
			replacement = string(data)
		}
		command := replaceCommand{redis.NewCmd(ctx, "eval", replaceIfUnchanged, 1, environmentsKey, field, value, replacement)}
		command.SetFirstKeyPos(3)
		if err := r.client.Process(ctx, command); err != nil {
			return errors.Wrap(err, "replace service environment")
		}
		changed, err := command.Int64()
		if err != nil {
			return errors.Wrap(err, "replace service environment")
		}
		if changed == 1 {
			return nil
		}
	}
}

// List returns immutable aggregate snapshots in no particular order.
func (r *Redis) List(ctx context.Context) ([]model.ServiceEnvironment, error) {
	values, err := r.client.HGetAll(ctx, environmentsKey).Result()
	if err != nil {
		return nil, errors.Wrap(err, "list service environments")
	}
	environments := make([]model.ServiceEnvironment, 0, len(values))
	for _, value := range values {
		environment, err := decodeEnvironment([]byte(value))
		if err != nil {
			return nil, err
		}
		environments = append(environments, environment)
	}
	return environments, nil
}

// Close disconnects from Redis.
func (r *Redis) Close() error { return errors.Wrap(r.client.Close(), "close Redis client") }

// decodeEnvironment rejects unknown fields throughout the persisted aggregate.
func decodeEnvironment(data []byte) (model.ServiceEnvironment, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var environment model.ServiceEnvironment
	if err := decoder.Decode(&environment); err != nil {
		return model.ServiceEnvironment{}, errors.Wrap(err, "decode service environment")
	}
	return environment, nil
}

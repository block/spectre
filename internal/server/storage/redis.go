package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/alecthomas/errors"
	"github.com/redis/go-redis/v9"

	"github.com/block/spectre/internal/model"
)

// divergencesKey is the Redis hash that maps each sourceField to its divergence
// as JSON.
const divergencesKey = "spectre:divergences"

// Redis keeps divergences in a Redis hash, so they outlive the SPECTRE server
// and are shared by every server using the same Redis database.
type Redis struct {
	client *redis.Client
}

var _ Store = (*Redis)(nil)

// NewRedis connects to the Redis server the configuration names.
func NewRedis(ctx context.Context, config Config) (*Redis, error) {
	options, err := redis.ParseURL(config.RedisURL)
	if err != nil {
		return nil, errors.Wrap(err, "redis URL")
	}
	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close() //nolint:errcheck // The connection failed, so closing has nothing to report.
		return nil, errors.Wrap(err, "connect to Redis")
	}
	return &Redis{client: client}, nil
}

// Record keeps a divergence unless its source has already reported a later quarantine.
func (r *Redis) Record(ctx context.Context, divergence model.Divergence) error {
	field := sourceField(divergence.Service, divergence.Environment, divergence.Direction)
	value, err := json.Marshal(divergence)
	if err != nil {
		return errors.Wrap(err, "encode divergence")
	}
	// The comparison happens in Go, so watch the hash and retry whenever another
	// write lands between reading the stored divergence and replacing it.
	for {
		err := r.client.Watch(ctx, func(tx *redis.Tx) error {
			existing, err := tx.HGet(ctx, divergencesKey, field).Bytes()
			switch {
			case errors.Is(err, redis.Nil):
			case err != nil:
				return errors.Wrap(err, "read divergence")
			default:
				stored, err := decodeDivergence(existing)
				if err != nil {
					return err
				}
				if quarantinedAfter(stored.Quarantine, divergence.Quarantine) {
					return nil
				}
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

// Clear removes the divergences of both directions of a service environment's mirror.
func (r *Redis) Clear(ctx context.Context, service, environment string) error {
	err := r.client.HDel(ctx, divergencesKey,
		sourceField(service, environment, model.Ingress),
		sourceField(service, environment, model.Egress),
	).Err()
	return errors.Wrap(err, "clear divergences")
}

// List returns every stored divergence, in no particular order.
func (r *Redis) List(ctx context.Context) ([]model.Divergence, error) {
	values, err := r.client.HVals(ctx, divergencesKey).Result()
	if err != nil {
		return nil, errors.Wrap(err, "list divergences")
	}
	divergences := make([]model.Divergence, 0, len(values))
	for _, value := range values {
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

// sourceField identifies the proxy of one direction of a service environment's
// mirror. Quoting keeps the parts unambiguous whatever characters they hold.
func sourceField(service, environment string, direction model.Direction) string {
	return strconv.Quote(service) + " " + strconv.Quote(environment) + " " + string(direction)
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

package storage

import (
	"context"
	"strconv"

	"github.com/alecthomas/errors"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	pb "github.com/block/spectre/spectrepb"
)

// divergencesKey is the Redis hash that maps each sourceField to its divergence.
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
func (r *Redis) Record(ctx context.Context, divergence *pb.Divergence) error {
	field := sourceField(divergence.GetService(), divergence.GetEnvironment(), divergence.GetDirection())
	value, err := proto.Marshal(divergence)
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
				stored := &pb.Divergence{}
				if err := proto.Unmarshal(existing, stored); err != nil {
					return errors.Wrap(err, "decode divergence")
				}
				if quarantinedAfter(stored, divergence) {
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
		sourceField(service, environment, pb.Direction_DIRECTION_INGRESS),
		sourceField(service, environment, pb.Direction_DIRECTION_EGRESS),
	).Err()
	return errors.Wrap(err, "clear divergences")
}

// List returns every stored divergence, in no particular order.
func (r *Redis) List(ctx context.Context) ([]*pb.Divergence, error) {
	values, err := r.client.HVals(ctx, divergencesKey).Result()
	if err != nil {
		return nil, errors.Wrap(err, "list divergences")
	}
	divergences := make([]*pb.Divergence, 0, len(values))
	for _, value := range values {
		divergence := &pb.Divergence{}
		if err := proto.Unmarshal([]byte(value), divergence); err != nil {
			return nil, errors.Wrap(err, "decode divergence")
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
func sourceField(service, environment string, direction pb.Direction) string {
	return strconv.Quote(service) + " " + strconv.Quote(environment) + " " + direction.String()
}

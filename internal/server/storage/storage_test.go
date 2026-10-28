package storage_test

import (
	"cmp"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alicebob/miniredis/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/server/storage"
	pb "github.com/block/spectre/spectrepb"
)

func TestStores(t *testing.T) {
	backends := []struct {
		name string
		open func(t *testing.T) storage.Store
	}{
		{name: "Memory", open: func(*testing.T) storage.Store { return storage.NewMemory() }},
		{name: "Redis", open: openRedis},
	}
	tests := []struct {
		name string
		test func(t *testing.T, store storage.Store)
	}{
		{name: "KeepsLatestQuarantine", test: testKeepsLatestQuarantine},
		{name: "ClearsServiceEnvironment", test: testClearsServiceEnvironment},
		{name: "RecordsConcurrently", test: testRecordsConcurrently},
	}
	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					store := backend.open(t)
					t.Cleanup(func() { assert.NoError(t, store.Close()) })
					test.test(t, store)
				})
			}
		})
	}
}

func TestOpen(t *testing.T) {
	server := miniredis.RunT(t)
	tests := []struct {
		name   string
		config storage.Config
		err    string
	}{
		{name: "Memory", config: storage.Config{Storage: storage.BackendMemory}},
		{name: "Redis", config: storage.Config{Storage: storage.BackendRedis, RedisURL: "redis://" + server.Addr()}},
		{name: "RedisWithoutURL", config: storage.Config{Storage: storage.BackendRedis}, err: "redis storage needs a Redis URL"},
		{name: "InvalidRedisURL", config: storage.Config{Storage: storage.BackendRedis, RedisURL: "http://localhost"}, err: "redis URL"},
		{name: "UnreachableRedis", config: storage.Config{Storage: storage.BackendRedis, RedisURL: "redis://127.0.0.1:1"}, err: "connect to Redis"},
		{name: "UnknownBackend", config: storage.Config{Storage: "disk"}, err: `unknown storage "disk"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := storage.Open(t.Context(), test.config)
			if test.err != "" {
				assert.Contains(t, err.Error(), test.err)
				return
			}
			assert.NoError(t, err)
			assert.NoError(t, store.Close())
		})
	}
}

func openRedis(t *testing.T) storage.Store {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := storage.NewRedis(t.Context(), storage.Config{Storage: storage.BackendRedis, RedisURL: "redis://" + server.Addr()})
	assert.NoError(t, err)
	return store
}

func testKeepsLatestQuarantine(t *testing.T, store storage.Store) {
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	latest := divergence("production", pb.Direction_DIRECTION_INGRESS, first.Add(time.Minute))
	egress := divergence("production", pb.Direction_DIRECTION_EGRESS, first)
	for _, reported := range []*pb.Divergence{
		divergence("production", pb.Direction_DIRECTION_INGRESS, first),
		latest,
		// A stale retry from before the mirror restarted.
		divergence("production", pb.Direction_DIRECTION_INGRESS, first.Add(-time.Minute)),
		egress,
	} {
		assert.NoError(t, store.Record(t.Context(), reported))
	}
	assertStored(t, store, latest, egress)
}

func testClearsServiceEnvironment(t *testing.T, store storage.Store) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	staging := divergence("staging", pb.Direction_DIRECTION_INGRESS, since)
	for _, reported := range []*pb.Divergence{
		divergence("production", pb.Direction_DIRECTION_INGRESS, since),
		divergence("production", pb.Direction_DIRECTION_EGRESS, since),
		staging,
	} {
		assert.NoError(t, store.Record(t.Context(), reported))
	}
	assert.NoError(t, store.Clear(t.Context(), "users", "production"))
	assertStored(t, store, staging)
}

// testRecordsConcurrently checks that concurrent writes to different sources
// are all kept, which makes the Redis store retry its transactions.
func testRecordsConcurrently(t *testing.T, store storage.Store) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	expected := []*pb.Divergence{}
	for i := range 20 {
		expected = append(expected, divergence("environment-"+strconv.Itoa(i), pb.Direction_DIRECTION_INGRESS, since))
	}
	var wg sync.WaitGroup
	for _, reported := range expected {
		wg.Go(func() { assert.NoError(t, store.Record(t.Context(), reported)) })
	}
	wg.Wait()
	assertStored(t, store, expected...)
}

func divergence(environment string, direction pb.Direction, since time.Time) *pb.Divergence {
	return &pb.Divergence{
		Service:     "users",
		Environment: environment,
		Revision:    "abc123",
		Direction:   direction,
		Quarantine:  &pb.Quarantine{Since: timestamppb.New(since), Kind: pb.Quarantine_KIND_CAPACITY},
	}
}

// assertStored checks the store holds exactly the expected divergences, in any order.
func assertStored(t *testing.T, store storage.Store, expected ...*pb.Divergence) {
	t.Helper()
	stored, err := store.List(t.Context())
	assert.NoError(t, err)
	bySource := func(a, b *pb.Divergence) int {
		return cmp.Or(cmp.Compare(a.GetEnvironment(), b.GetEnvironment()), cmp.Compare(a.GetDirection(), b.GetDirection()))
	}
	slices.SortFunc(stored, bySource)
	expected = slices.SortedFunc(slices.Values(expected), bySource)
	assert.True(t, proto.Equal(&pb.ListDivergencesResponse{Divergences: expected}, &pb.ListDivergencesResponse{Divergences: stored}), "%v", stored)
}

package storage_test

import (
	"cmp"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"github.com/alicebob/miniredis/v2"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/storage"
)

func TestStores(t *testing.T) {
	backends := []struct {
		name string
		open func(t *testing.T) storage.Store
	}{
		{name: "Memory", open: func(*testing.T) storage.Store { return storage.NewMemory() }},
		{name: "Redis", open: openRedis("redis")},
		{name: "RedisCluster", open: openRedis("redis-cluster")},
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
		name string
		url  string
		err  string
	}{
		{name: "Memory", url: "memory://"},
		{name: "Redis", url: "redis://" + server.Addr()},
		{name: "RedisCluster", url: "redis-cluster://" + server.Addr()},
		{name: "InvalidRedisURL", url: "redis://localhost/notadatabase", err: "redis URL"},
		{name: "RedisClusterDatabase", url: "redis-cluster://" + server.Addr() + "/1", err: "no databases"},
		{name: "InvalidRedisClusterURL", url: "redis-cluster://" + server.Addr() + "?db=1", err: "unexpected option: db"},
		{name: "UnreachableRedis", url: "redis://127.0.0.1:1", err: "connect to Redis"},
		{name: "UnreachableRedisCluster", url: "redis-cluster://127.0.0.1:1", err: "connect to Redis"},
		{name: "UnknownScheme", url: "disk:///var/lib/spectre", err: `unknown storage scheme "disk"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := storage.Open(t.Context(), storage.Config{Storage: parseURL(t, test.url)})
			if test.err != "" {
				assert.Contains(t, err.Error(), test.err)
				return
			}
			assert.NoError(t, err)
			assert.NoError(t, store.Close())
		})
	}
}

func TestOpenRequiresURL(t *testing.T) {
	_, err := storage.Open(t.Context(), storage.Config{})
	assert.EqualError(t, err, "storage URL is required")
}

func TestRedisRejectsUnknownFields(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := storage.NewRedis(t.Context(), parseURL(t, "redis://"+server.Addr()))
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	server.HSet("spectre:divergences", `"users" "production" ingress`,
		`{"service":"users","quarantine":{"reason":{"kind":"divergent","traffic":{"direction":"ingress","method":"GET"}}}}`)
	_, err = store.List(t.Context())
	assert.Contains(t, err.Error(), `unknown field "method"`)
}

// openRedis opens a store on a fresh miniredis, which acts as a cluster of one
// node owning every slot when the scheme selects a cluster client.
func openRedis(scheme string) func(t *testing.T) storage.Store {
	return func(t *testing.T) storage.Store {
		t.Helper()
		server := miniredis.RunT(t)
		store, err := storage.NewRedis(t.Context(), parseURL(t, scheme+"://"+server.Addr()))
		assert.NoError(t, err)
		return store
	}
}

func parseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	assert.NoError(t, err)
	return parsed
}

func testKeepsLatestQuarantine(t *testing.T, store storage.Store) {
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	latest := divergence("production", model.Ingress, first.Add(time.Minute))
	latest.Quarantine.Reason = model.QuarantineReason{
		Kind:        model.QuarantineDivergent,
		Traffic:     Some(model.Traffic{Direction: model.Ingress, Protocol: model.ProtocolHTTP, Route: "GET /v1/users"}),
		Node:        Some("users"),
		Differences: []string{"body.name"},
	}
	egress := divergence("production", model.Egress, first)
	for _, reported := range []model.Divergence{
		divergence("production", model.Ingress, first),
		latest,
		// A stale retry from before the mirror restarted.
		divergence("production", model.Ingress, first.Add(-time.Minute)),
		egress,
	} {
		assert.NoError(t, store.Record(t.Context(), reported))
	}
	assertStored(t, store, latest, egress)
}

func testClearsServiceEnvironment(t *testing.T, store storage.Store) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	staging := divergence("staging", model.Ingress, since)
	for _, reported := range []model.Divergence{
		divergence("production", model.Ingress, since),
		divergence("production", model.Egress, since),
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
	expected := []model.Divergence{}
	for i := range 20 {
		expected = append(expected, divergence("environment-"+strconv.Itoa(i), model.Ingress, since))
	}
	var wg sync.WaitGroup
	for _, reported := range expected {
		wg.Go(func() { assert.NoError(t, store.Record(t.Context(), reported)) })
	}
	wg.Wait()
	assertStored(t, store, expected...)
}

func divergence(environment string, direction model.Direction, since time.Time) model.Divergence {
	return model.Divergence{
		Service:     "users",
		Environment: environment,
		Revision:    "abc123",
		Direction:   direction,
		Quarantine:  model.Quarantine{Since: since, Reason: model.QuarantineReason{Kind: model.QuarantineCapacity}},
	}
}

// assertStored checks the store holds exactly the expected divergences, in any order.
func assertStored(t *testing.T, store storage.Store, expected ...model.Divergence) {
	t.Helper()
	stored, err := store.List(t.Context())
	assert.NoError(t, err)
	bySource := func(a, b model.Divergence) int {
		return cmp.Or(cmp.Compare(a.Environment, b.Environment), cmp.Compare(a.Direction, b.Direction))
	}
	slices.SortFunc(stored, bySource)
	assert.Equal(t, slices.SortedFunc(slices.Values(expected), bySource), stored)
}

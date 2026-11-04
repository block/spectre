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
		{name: "ReplacesState", test: testReplacesState},
		{name: "ForgetsStaleStates", test: testForgetsStaleStates},
		{name: "PutsConcurrently", test: testPutsConcurrently},
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
	server.HSet("spectre:states", `"users" "production"`,
		`{"state":{"service":"users","quarantines":{"ingress":{"reason":{"kind":"divergent","traffic":{"direction":"ingress","method":"GET"}}}}}}`)
	_, err = store.List(t.Context(), time.Time{})
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

func testReplacesState(t *testing.T, store storage.Store) {
	received := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	latest := state("production", 2*time.Minute)
	//nolint:exhaustive // Only the ingress proxy quarantined.
	latest.Quarantines = map[model.Direction]model.Quarantine{model.Ingress: {
		Since: received,
		Reason: model.QuarantineReason{
			Kind:        model.QuarantineDivergent,
			Traffic:     Some(model.Traffic{Direction: model.Ingress, Protocol: model.ProtocolHTTP, Route: "GET /v1/users"}),
			Node:        Some("users"),
			Differences: []string{"body.name"},
		},
	}}
	staging := state("staging", time.Minute)
	assert.NoError(t, store.Put(t.Context(), state("production", time.Minute), received))
	assert.NoError(t, store.Put(t.Context(), latest, received.Add(time.Minute)))
	assert.NoError(t, store.Put(t.Context(), staging, received))
	assertStored(t, store, received, latest, staging)
}

func testForgetsStaleStates(t *testing.T, store storage.Store) {
	received := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	staging := state("staging", time.Minute)
	assert.NoError(t, store.Put(t.Context(), state("production", time.Minute), received))
	assert.NoError(t, store.Put(t.Context(), staging, received.Add(time.Minute)))
	assertStored(t, store, received.Add(time.Second), staging)
	// The stale state stays forgotten when an older cutoff would include it.
	assertStored(t, store, received, staging)
}

// testPutsConcurrently checks that concurrent writes for different service
// environments are all kept.
func testPutsConcurrently(t *testing.T, store storage.Store) {
	received := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	expected := []model.State{}
	for i := range 20 {
		expected = append(expected, state("environment-"+strconv.Itoa(i), time.Minute))
	}
	var wg sync.WaitGroup
	for _, reported := range expected {
		wg.Go(func() { assert.NoError(t, store.Put(t.Context(), reported, received)) })
	}
	wg.Wait()
	assertStored(t, store, received, expected...)
}

func state(environment string, uptime time.Duration) model.State {
	return model.State{
		Service:         "users",
		Environment:     environment,
		CandidateCommit: "abc123",
		SpectreVersion:  "v1.2.3",
		Uptime:          uptime,
	}
}

// assertStored checks the store lists exactly the expected states received at
// or after since, in any order.
func assertStored(t *testing.T, store storage.Store, since time.Time, expected ...model.State) {
	t.Helper()
	stored, err := store.List(t.Context(), since)
	assert.NoError(t, err)
	byEnvironment := func(a, b model.State) int { return cmp.Compare(a.Environment, b.Environment) }
	slices.SortFunc(stored, byEnvironment)
	assert.Equal(t, slices.SortedFunc(slices.Values(expected), byEnvironment), stored)
}

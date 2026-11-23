package storage_test

import (
	"cmp"
	"context"
	"maps"
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
		{name: "KeepsLatestQuarantine", test: testKeepsLatestQuarantine},
		{name: "ClearsServiceEnvironment", test: testClearsServiceEnvironment},
		{name: "RecordsConcurrently", test: testRecordsConcurrently},
		{name: "AddsUpRuns", test: testAddsUpRuns},
		{name: "RestartsTally", test: testRestartsTally},
		{name: "MergesQuietRuns", test: testMergesQuietRuns},
		{name: "ForgetsStaleTallies", test: testForgetsStaleTallies},
		{name: "CountsConcurrently", test: testCountsConcurrently},
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
	tests := []struct {
		name  string
		key   string
		field string
		value string
		list  func(ctx context.Context, store storage.Store) error
	}{
		{
			name:  "State",
			key:   "spectre:states",
			field: `"users" "production"`,
			value: `{"state":{"service":"users","quarantines":{"ingress":{"reason":{"kind":"divergent","traffic":{"direction":"ingress","method":"GET"}}}}}}`,
			list: func(ctx context.Context, store storage.Store) error {
				_, err := store.States(ctx, time.Time{})
				return err
			},
		},
		{
			name:  "Tally",
			key:   "spectre:calls",
			field: `"users" "production"`,
			value: `{"service":"users","environment":"production","runs":{"a":{"latest":{"routes":{"GET /v1/users":1}},"method":"GET"}}}`,
			list: func(ctx context.Context, store storage.Store) error {
				_, err := store.Tallies(ctx, time.Time{})
				return err
			},
		},
		{
			name:  "Divergence",
			key:   "spectre:divergences",
			field: `"users" "production" ingress`,
			value: `{"service":"users","quarantine":{"reason":{"kind":"divergent","traffic":{"direction":"ingress","method":"GET"}}}}`,
			list: func(ctx context.Context, store storage.Store) error {
				_, err := store.Divergences(ctx)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			store, err := storage.NewRedis(t.Context(), parseURL(t, "redis://"+server.Addr()))
			assert.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, store.Close()) })
			server.HSet(test.key, test.field, test.value)
			err = test.list(t.Context(), store)
			assert.Contains(t, err.Error(), `unknown field "method"`)
		})
	}
}

// TestRedisReadsRecordedDivergences checks that divergences already stored
// in Redis stay readable.
func TestRedisReadsRecordedDivergences(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := storage.NewRedis(t.Context(), parseURL(t, "redis://"+server.Addr()))
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	server.HSet("spectre:divergences", `"users" "production" egress`,
		`{"service":"users","environment":"production","revision":"abc123","direction":"egress",`+
			`"quarantine":{"reason":{"kind":"unmatched","detail":"no matching reference call"},"since":"2026-10-01T12:00:00Z"}}`)
	divergences, err := store.Divergences(t.Context())
	assert.NoError(t, err)
	expected := []model.Divergence{{
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
		Direction:   model.Egress,
		Quarantine: model.Quarantine{
			Since:  time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
			Reason: model.QuarantineReason{Kind: model.QuarantineUnmatched, Detail: "no matching reference call"},
		},
	}}
	assert.Equal(t, expected, divergences)
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
			Kind:    model.QuarantineDivergent,
			Traffic: Some(model.Traffic{Direction: model.Ingress, Protocol: model.ProtocolHTTP, Route: "GET /v1/users"}),
			Node:    Some("users"),
			Differences: []model.Difference{
				{Path: "$.name", Change: model.ChangeChanged, Count: 1},
				{Path: "$.emails[*]", Change: model.ChangeMissing, Count: 2},
				{Path: "$.owner", Change: model.ChangeRetyped, Retype: Some(model.Retype{Reference: model.JSONObject, Candidate: model.JSONNull}), Count: 1},
				{Path: "$status", Change: model.ChangeChanged, Statuses: Some(model.Statuses{Reference: "200", Candidate: "500"}), Count: 1},
			},
			Truncated: true,
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
		Service:        "users",
		Environment:    environment,
		Revision:       "abc123",
		SpectreVersion: "v1.2.3",
		Uptime:         uptime,
	}
}

// assertStored checks the store lists exactly the expected states received at
// or after since, in any order.
func assertStored(t *testing.T, store storage.Store, since time.Time, expected ...model.State) {
	t.Helper()
	stored, err := store.States(t.Context(), since)
	assert.NoError(t, err)
	byEnvironment := func(a, b model.State) int { return cmp.Compare(a.Environment, b.Environment) }
	slices.SortFunc(stored, byEnvironment)
	assert.Equal(t, slices.SortedFunc(slices.Values(expected), byEnvironment), stored)
}

func testKeepsLatestQuarantine(t *testing.T, store storage.Store) {
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	latest := divergence("production", model.Ingress, first.Add(time.Minute))
	latest.Quarantine.Reason = model.QuarantineReason{
		Kind:    model.QuarantineDivergent,
		Traffic: Some(model.Traffic{Direction: model.Ingress, Protocol: model.ProtocolHTTP, Route: "GET /v1/users"}),
		Node:    Some("users"),
		Differences: []model.Difference{
			{Path: "$.name", Change: model.ChangeChanged, Count: 1},
			{Path: "$.emails[*]", Change: model.ChangeMissing, Count: 2},
			{Path: "$.owner", Change: model.ChangeRetyped, Retype: Some(model.Retype{Reference: model.JSONObject, Candidate: model.JSONNull}), Count: 1},
			{Path: "$status", Change: model.ChangeChanged, Statuses: Some(model.Statuses{Reference: "200", Candidate: "500"}), Count: 1},
		},
		Truncated: true,
	}
	egress := divergence("production", model.Egress, first)
	for _, reported := range []model.Divergence{
		divergence("production", model.Ingress, first),
		latest,
		// An update from before the mirror restarted and quarantined again.
		divergence("production", model.Ingress, first.Add(-time.Minute)),
		egress,
		// Every update repeats the quarantine.
		egress,
	} {
		assert.NoError(t, store.Record(t.Context(), reported))
	}
	assertRecorded(t, store, latest, egress)
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
	assert.NoError(t, store.Clear(t.Context(), "users", "production", since.Add(time.Minute)))
	assertRecorded(t, store, staging)

	// A mirror still quarantined keeps sending the cleared quarantine.
	assert.NoError(t, store.Record(t.Context(), divergence("production", model.Ingress, since)))
	assertRecorded(t, store, staging)
	later := divergence("production", model.Ingress, since.Add(2*time.Minute))
	assert.NoError(t, store.Record(t.Context(), later))
	assertRecorded(t, store, later, staging)
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
	assertRecorded(t, store, expected...)
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

// assertRecorded checks the store holds exactly the expected divergences, in any order.
func assertRecorded(t *testing.T, store storage.Store, expected ...model.Divergence) {
	t.Helper()
	recorded, err := store.Divergences(t.Context())
	assert.NoError(t, err)
	bySource := func(a, b model.Divergence) int {
		return cmp.Or(cmp.Compare(a.Environment, b.Environment), cmp.Compare(a.Direction, b.Direction))
	}
	slices.SortFunc(recorded, bySource)
	assert.Equal(t, slices.SortedFunc(slices.Values(expected), bySource), recorded)
}

func calls(routes map[model.Route]uint64) model.Calls {
	return model.Calls{Routes: routes}
}

// testAddsUpRuns checks that a redeployed mirror's calls add to the earlier
// run's, and that each run's latest counts replace its earlier ones.
func testAddsUpRuns(t *testing.T, store storage.Store) {
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ctx := t.Context()
	assert.NoError(t, store.Count(ctx, "users", "production", "a", calls(map[model.Route]uint64{"GET /v1/users": 1}), first))
	assert.NoError(t, store.Count(ctx, "users", "production", "a", calls(map[model.Route]uint64{"GET /v1/users": 3}), first.Add(time.Minute)))
	assert.NoError(t, store.Count(ctx, "users", "production", "b", calls(map[model.Route]uint64{"GET /v1/users": 2, "POST /v1/users": 1}), first.Add(2*time.Minute)))
	// A reconnecting run resends the counts it already sent.
	assert.NoError(t, store.Count(ctx, "users", "production", "b", calls(map[model.Route]uint64{"GET /v1/users": 2, "POST /v1/users": 1}), first.Add(3*time.Minute)))
	assert.NoError(t, store.Count(ctx, "users", "staging", "c", model.Calls{Other: 1}, first))
	tally := assertTallied(t, store, first,
		model.CallTotal{Service: "users", Environment: "production", Since: first, Start: model.TotalFirstReport, Calls: calls(map[model.Route]uint64{"GET /v1/users": 5, "POST /v1/users": 1}), Runs: 2},
		model.CallTotal{Service: "users", Environment: "staging", Since: first, Start: model.TotalFirstReport, Calls: model.Calls{Other: 1}, Runs: 1},
	)
	assert.Equal(t, calls(map[model.Route]uint64{"GET /v1/users": 2, "POST /v1/users": 1}), tally["production"].RunCalls("b"))
}

// testRestartsTally checks that a restart counts only calls reported after it,
// and that a restart from before the tally's start changes nothing.
func testRestartsTally(t *testing.T, store storage.Store) {
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ctx := t.Context()
	assert.NoError(t, store.Count(ctx, "users", "production", "a", calls(map[model.Route]uint64{"GET /v1/users": 3}), first))
	assert.NoError(t, store.Restart(ctx, "users", "production", model.TotalQuarantine, first.Add(time.Minute)))
	assert.NoError(t, store.Restart(ctx, "users", "production", model.TotalQuarantine, first.Add(-time.Minute)))
	quarantined := model.CallTotal{Service: "users", Environment: "production", Since: first.Add(time.Minute), Start: model.TotalQuarantine}
	assertTallied(t, store, first, quarantined)

	assert.NoError(t, store.Count(ctx, "users", "production", "a", calls(map[model.Route]uint64{"GET /v1/users": 5}), first.Add(2*time.Minute)))
	assert.NoError(t, store.Count(ctx, "users", "production", "b", calls(map[model.Route]uint64{"GET /v1/users": 1}), first.Add(2*time.Minute)))
	quarantined.Calls, quarantined.Runs = calls(map[model.Route]uint64{"GET /v1/users": 3}), 2
	tally := assertTallied(t, store, first, quarantined)
	assert.Equal(t, calls(map[model.Route]uint64{"GET /v1/users": 2}), tally["production"].RunCalls("a"))

	// A clear restarts a service environment with no tally too.
	assert.NoError(t, store.Restart(ctx, "users", "production", model.TotalClear, first.Add(3*time.Minute)))
	assert.NoError(t, store.Restart(ctx, "users", "staging", model.TotalClear, first.Add(3*time.Minute)))
	assertTallied(t, store, first,
		model.CallTotal{Service: "users", Environment: "production", Since: first.Add(3 * time.Minute), Start: model.TotalClear},
		model.CallTotal{Service: "users", Environment: "staging", Since: first.Add(3 * time.Minute), Start: model.TotalClear},
	)
}

// testMergesQuietRuns checks that runs that stopped reporting a day ago still
// count once merged.
func testMergesQuietRuns(t *testing.T, store storage.Store) {
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ctx := t.Context()
	assert.NoError(t, store.Count(ctx, "users", "production", "a", calls(map[model.Route]uint64{"GET /v1/users": 3}), first))
	assert.NoError(t, store.Count(ctx, "users", "production", "idle", model.Calls{}, first))
	later := first.Add(25 * time.Hour)
	assert.NoError(t, store.Count(ctx, "users", "production", "b", calls(map[model.Route]uint64{"GET /v1/users": 1}), later))
	tally := assertTallied(t, store, first,
		model.CallTotal{Service: "users", Environment: "production", Since: first, Start: model.TotalFirstReport, Calls: calls(map[model.Route]uint64{"GET /v1/users": 4}), Runs: 2},
	)
	assert.Equal(t, []string{"b"}, slices.Sorted(maps.Keys(tally["production"].Runs)))
}

func testForgetsStaleTallies(t *testing.T, store storage.Store) {
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ctx := t.Context()
	assert.NoError(t, store.Count(ctx, "users", "production", "a", model.Calls{Other: 1}, first))
	assert.NoError(t, store.Count(ctx, "users", "staging", "b", model.Calls{Other: 1}, first.Add(time.Minute)))
	staging := model.CallTotal{Service: "users", Environment: "staging", Since: first.Add(time.Minute), Start: model.TotalFirstReport, Calls: model.Calls{Other: 1}, Runs: 1}
	assertTallied(t, store, first.Add(time.Second), staging)
	assertTallied(t, store, first, staging)
}

// testCountsConcurrently checks that concurrent counts of one service
// environment are all kept, which makes the Redis store retry its transactions.
func testCountsConcurrently(t *testing.T, store storage.Store) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			assert.NoError(t, store.Count(t.Context(), "users", "production", strconv.Itoa(i), model.Calls{Other: 1}, at))
		})
	}
	wg.Wait()
	assertTallied(t, store, at, model.CallTotal{Service: "users", Environment: "production", Since: at, Start: model.TotalFirstReport, Calls: model.Calls{Other: 20}, Runs: 20})
}

// assertTallied checks the store's totals updated at or after since, in any
// order, and returns the tallies by environment.
func assertTallied(t *testing.T, store storage.Store, since time.Time, expected ...model.CallTotal) map[string]storage.Tally {
	t.Helper()
	tallies, err := store.Tallies(t.Context(), since)
	assert.NoError(t, err)
	byEnvironment := map[string]storage.Tally{}
	totals := []model.CallTotal{}
	for _, tally := range tallies {
		byEnvironment[tally.Environment] = tally
		totals = append(totals, tally.Total())
	}
	order := func(a, b model.CallTotal) int { return cmp.Compare(a.Environment, b.Environment) }
	slices.SortFunc(totals, order)
	assert.Equal(t, slices.SortedFunc(slices.Values(expected), order), totals)
	return byEnvironment
}

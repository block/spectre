package storage_test

import (
	"cmp"
	"context"
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
		test func(t *testing.T, store *storage.Repository)
	}{
		{name: "ReplacesState", test: testReplacesState},
		{name: "ForgetsStaleStates", test: testForgetsStaleStates},
		{name: "PutsConcurrently", test: testPutsConcurrently},
		{name: "KeepsLatestQuarantine", test: testKeepsLatestQuarantine},
		{name: "ClearsServiceEnvironment", test: testClearsServiceEnvironment},
		{name: "RecordsConcurrently", test: testRecordsConcurrently},
		{name: "CallTotals", test: testCallTotals},
		{name: "CountsConcurrently", test: testCountsConcurrently},
	}
	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) {
			t.Run("UpdatesSameAggregate", func(t *testing.T) {
				store := backend.open(t)
				t.Cleanup(func() { assert.NoError(t, store.Close()) })
				testUpdatesSameAggregate(t, store)
			})
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					store := storage.NewRepository(backend.open(t))
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
		list  func(ctx context.Context, store *storage.Repository) error
	}{
		{
			name:  "State",
			key:   "spectre:environments",
			field: `"users" "production"`,
			value: `{"state":{"service":"users","quarantines":{"ingress":{"reason":{"kind":"divergent","traffic":{"direction":"ingress","method":"GET"}}}}}}`,
			list: func(ctx context.Context, store *storage.Repository) error {
				_, err := states(ctx, store, time.Time{})
				return err
			},
		},
		{
			name:  "Divergence",
			key:   "spectre:environments",
			field: `"users" "production"`,
			value: `{"service":"users","environment":"production","divergences":[{"service":"users","quarantine":{"reason":{"kind":"divergent","traffic":{"direction":"ingress","method":"GET"}}}}]}`,
			list: func(ctx context.Context, store *storage.Repository) error {
				_, err := divergences(ctx, store)
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
			err = test.list(t.Context(), storage.NewRepository(store))
			assert.Contains(t, err.Error(), `unknown field "method"`)
		})
	}
}

// TestRedisReadsAggregateHistory restores divergence history from a stored aggregate.
func TestRedisReadsAggregateHistory(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := storage.NewRedis(t.Context(), parseURL(t, "redis://"+server.Addr()))
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	server.HSet("spectre:environments", `"users" "production"`,
		`{"service":"users","environment":"production","divergences":[{"service":"users","environment":"production","revision":"abc123","direction":"egress",`+
			`"quarantine":{"reason":{"kind":"unmatched","detail":"no matching reference call"},"since":"2026-10-01T12:00:00Z"}}]}`)
	divergences, err := divergences(t.Context(), storage.NewRepository(store))
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

func testReplacesState(t *testing.T, store *storage.Repository) {
	received := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	latest := state("production", 2*time.Minute)
	//nolint:exhaustive // Only the ingress proxy quarantined.
	latest.Quarantines = map[model.Direction]model.Quarantine{model.Ingress: {
		Since: received,
		Reason: model.QuarantineReason{
			Kind:    model.QuarantineDivergent,
			Traffic: Some(model.Traffic{Direction: model.Ingress, Protocol: "fake", Route: "GET /v1/users"}),
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
	assert.NoError(t, store.Observe(t.Context(), state("production", time.Minute), received, time.Time{}, model.CallsReport))
	assert.NoError(t, store.Observe(t.Context(), latest, received.Add(time.Minute), time.Time{}, model.CallsReport))
	assert.NoError(t, store.Observe(t.Context(), staging, received, time.Time{}, model.CallsReport))
	assertStored(t, store, received, latest, staging)
	status, err := store.List(t.Context(), received, time.Time{})
	assert.NoError(t, err)
	assert.Equal(t, 0, len(status.Divergences))
}

func testForgetsStaleStates(t *testing.T, store *storage.Repository) {
	received := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	staging := state("staging", time.Minute)
	quarantine := divergence("production", model.Ingress, received)
	assert.NoError(t, record(t.Context(), store, quarantine))
	assert.NoError(t, store.Observe(t.Context(), staging, received.Add(time.Minute), time.Time{}, model.CallsReport))
	assertStored(t, store, received.Add(time.Second), staging)
	// The stale state stays forgotten when an older cutoff would include it.
	assertStored(t, store, received, staging)
	assertRecorded(t, store, quarantine)
}

// testPutsConcurrently checks that concurrent writes for different service
// environments are all kept.
func testPutsConcurrently(t *testing.T, store *storage.Repository) {
	received := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	expected := []model.State{}
	for i := range 20 {
		expected = append(expected, state("environment-"+strconv.Itoa(i), time.Minute))
	}
	var wg sync.WaitGroup
	for _, reported := range expected {
		wg.Go(func() {
			assert.NoError(t, store.Observe(t.Context(), reported, received, time.Time{}, model.CallsReport))
		})
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
func assertStored(t *testing.T, store *storage.Repository, since time.Time, expected ...model.State) {
	t.Helper()
	stored, err := states(t.Context(), store, since)
	assert.NoError(t, err)
	byEnvironment := func(a, b model.State) int { return cmp.Compare(a.Environment, b.Environment) }
	slices.SortFunc(stored, byEnvironment)
	assert.Equal(t, slices.SortedFunc(slices.Values(expected), byEnvironment), stored)
}

func testKeepsLatestQuarantine(t *testing.T, store *storage.Repository) {
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	latest := divergence("production", model.Ingress, first.Add(time.Minute))
	latest.Quarantine.Reason = model.QuarantineReason{
		Kind:    model.QuarantineDivergent,
		Traffic: Some(model.Traffic{Direction: model.Ingress, Protocol: "fake", Route: "GET /v1/users"}),
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
		assert.NoError(t, record(t.Context(), store, reported))
	}
	assertRecorded(t, store, latest, egress)
}

func testClearsServiceEnvironment(t *testing.T, store *storage.Repository) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	staging := divergence("staging", model.Ingress, since)
	for _, reported := range []model.Divergence{
		divergence("production", model.Ingress, since),
		divergence("production", model.Egress, since),
		staging,
	} {
		assert.NoError(t, record(t.Context(), store, reported))
	}
	assert.NoError(t, store.Clear(t.Context(), "users", "production", since.Add(time.Minute), time.Time{}))
	assertRecorded(t, store, staging)

	// Expiring live state retains the clear watermark and divergence history.
	expired, err := store.List(t.Context(), since.Add(time.Hour), time.Time{})
	assert.NoError(t, err)
	assert.Equal(t, model.Status{States: []model.State{}, Divergences: []model.Divergence{staging}, CallTotals: []model.CallTotal{
		{Service: "users", Environment: "production", Since: since.Add(time.Minute), Start: model.TotalClear},
		{Service: "users", Environment: "staging", Since: since, Start: model.TotalQuarantine},
	}}, expired.Filter(model.DivergenceFilter{}))
	// A mirror still quarantined keeps sending the cleared quarantine.
	assert.NoError(t, record(t.Context(), store, divergence("production", model.Ingress, since)))
	assertRecorded(t, store, staging)
	later := divergence("production", model.Ingress, since.Add(2*time.Minute))
	assert.NoError(t, record(t.Context(), store, later))
	assertRecorded(t, store, later, staging)
}

// testRecordsConcurrently checks that reports for independent environments
// are all retained.
func testRecordsConcurrently(t *testing.T, store *storage.Repository) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	expected := []model.Divergence{}
	for i := range 20 {
		expected = append(expected, divergence("environment-"+strconv.Itoa(i), model.Ingress, since))
	}
	var wg sync.WaitGroup
	for _, reported := range expected {
		wg.Go(func() { assert.NoError(t, record(t.Context(), store, reported)) })
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
func assertRecorded(t *testing.T, store *storage.Repository, expected ...model.Divergence) {
	t.Helper()
	recorded, err := divergences(t.Context(), store)
	assert.NoError(t, err)
	bySource := func(a, b model.Divergence) int {
		return cmp.Or(cmp.Compare(a.Environment, b.Environment), cmp.Compare(a.Direction, b.Direction))
	}
	slices.SortFunc(recorded, bySource)
	assert.Equal(t, slices.SortedFunc(slices.Values(expected), bySource), recorded)
}

// record reports a fixture's quarantine through the repository's observation boundary.
func record(ctx context.Context, store *storage.Repository, divergence model.Divergence) error {
	return store.Observe(ctx, model.State{
		Service: divergence.Service, Environment: divergence.Environment, Revision: divergence.Revision,
		//nolint:exhaustive // Only the reported direction quarantined.
		Quarantines: map[model.Direction]model.Quarantine{divergence.Direction: divergence.Quarantine},
	}, divergence.Quarantine.Since, time.Time{}, model.StateReport)
}

func states(ctx context.Context, store *storage.Repository, since time.Time) ([]model.State, error) {
	status, err := store.List(ctx, since, time.Time{})
	return status.States, err
}

func divergences(ctx context.Context, store *storage.Repository) ([]model.Divergence, error) {
	status, err := store.List(ctx, time.Time{}, time.Time{})
	return status.Divergences, err
}

func testUpdatesSameAggregate(t *testing.T, store storage.Store) {
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			assert.NoError(t, store.Update(t.Context(), "users", "production", func(e model.ServiceEnvironment) Option[model.ServiceEnvironment] {
				e.Received = e.Received.Add(time.Second)
				return Some(e)
			}))
		})
	}
	group.Wait()
	stored, err := store.List(t.Context())
	assert.NoError(t, err)
	assert.Equal(t, []model.ServiceEnvironment{{EnvironmentStatus: model.EnvironmentStatus{Service: "users", Environment: "production"}, Received: time.Time{}.Add(20 * time.Second)}}, stored)
	assert.NoError(t, store.Update(t.Context(), "users", "production", func(_ model.ServiceEnvironment) Option[model.ServiceEnvironment] {
		return None[model.ServiceEnvironment]()
	}))
	stored, err = store.List(t.Context())
	assert.NoError(t, err)
	assert.Equal(t, 0, len(stored))
}

func TestRedisRejectsPreviousFormatWithoutChangingRecords(t *testing.T) {
	for _, key := range []string{"spectre:states", "spectre:divergences", "spectre:calls:{spectre:divergences}"} {
		t.Run(key, func(t *testing.T) {
			server := miniredis.RunT(t)
			server.HSet(key, "retained", "original")
			_, err := storage.NewRedis(t.Context(), parseURL(t, "redis://"+server.Addr()))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "previous-format")
			assert.Equal(t, "original", server.HGet(key, "retained"))
		})
	}
}

func testCallTotals(t *testing.T, store *storage.Repository) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	report := func(run string, count uint64, minutes int) {
		state := state("production", time.Minute)
		state.Run, state.Calls = run, model.Calls{Other: count}
		assert.NoError(t, store.Observe(t.Context(), state, at.Add(time.Duration(minutes)*time.Minute), time.Time{}, model.CallsReport))
	}
	check := func(count uint64, runs int, start model.TotalStart, since time.Time) {
		listed, err := store.List(t.Context(), time.Time{}, time.Time{})
		assert.NoError(t, err)
		assert.Equal(t, []model.CallTotal{{Service: "users", Environment: "production", Since: since, Start: start, Calls: model.Calls{Other: count}, Runs: runs}}, listed.CallTotals)
	}
	report("a", 3, 0)
	report("a", 2, 1) // A delayed older report never lowers its checkpoint.
	report("a", 3, 1)
	report("b", 2, 1)
	check(5, 2, model.TotalFirstReport, at)
	expired, err := store.List(t.Context(), at.Add(time.Hour), time.Time{})
	assert.NoError(t, err)
	assert.Equal(t, 0, len(expired.States))
	check(5, 2, model.TotalFirstReport, at)
	quarantined := state("production", time.Minute)
	quarantined.Run = "b"
	//nolint:exhaustive // Only ingress quarantined.
	quarantined.Quarantines = map[model.Direction]model.Quarantine{model.Ingress: {Since: at.Add(2 * time.Minute)}}
	assert.NoError(t, store.Observe(t.Context(), quarantined, at.Add(2*time.Minute), time.Time{}, model.StateReport))
	check(0, 0, model.TotalQuarantine, at.Add(2*time.Minute))
	report("a", 5, 3)
	report("b", 99, 3) // A delayed healthy snapshot cannot revive a quarantined run.
	check(2, 1, model.TotalQuarantine, at.Add(2*time.Minute))
	assert.NoError(t, store.Clear(t.Context(), "users", "production", at.Add(4*time.Minute), time.Time{}))
	assert.NoError(t, store.Observe(t.Context(), quarantined, at.Add(5*time.Minute), time.Time{}, model.StateReport))
	report("a", 6, 6)
	check(1, 1, model.TotalClear, at.Add(4*time.Minute))
	expired, err = store.List(t.Context(), time.Time{}, at.Add(7*time.Minute))
	assert.NoError(t, err)
	assert.Equal(t, 0, len(expired.CallTotals))
	expired, err = store.List(t.Context(), time.Time{}, time.Time{})
	assert.NoError(t, err)
	assert.Equal(t, 0, len(expired.CallTotals))
	report("a", 7, 8)
	check(7, 1, model.TotalFirstReport, at.Add(8*time.Minute))
	// A new report also expires inactive history without an intervening list.
	fresh := state("production", time.Minute)
	fresh.Run, fresh.Calls = "a", model.Calls{Other: 8}
	assert.NoError(t, store.Observe(t.Context(), fresh, at.Add(10*time.Minute), at.Add(9*time.Minute), model.CallsReport))
	check(8, 1, model.TotalFirstReport, at.Add(10*time.Minute))
	assert.NoError(t, store.Clear(t.Context(), "users", "production", at.Add(12*time.Minute), at.Add(11*time.Minute)))
	fresh.Calls.Other = 9
	assert.NoError(t, store.Observe(t.Context(), fresh, at.Add(13*time.Minute), time.Time{}, model.CallsReport))
	check(9, 1, model.TotalClear, at.Add(12*time.Minute))
	// Heartbeats refresh live state without extending call-history retention.
	assert.NoError(t, store.Observe(t.Context(), fresh, at.Add(20*time.Minute), at.Add(19*time.Minute), model.Keepalive))
	expired, err = store.List(t.Context(), at.Add(19*time.Minute), at.Add(14*time.Minute))
	assert.NoError(t, err)
	assert.Equal(t, 0, len(expired.CallTotals))
	assert.Equal(t, model.Calls{Other: 9}, expired.States[0].Calls)
}

func testCountsConcurrently(t *testing.T, store *storage.Repository) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var group sync.WaitGroup
	for i := range 20 {
		group.Go(func() {
			state := state("production", time.Minute)
			state.Run, state.Calls = strconv.Itoa(i), model.Calls{Other: 1}
			assert.NoError(t, store.Observe(t.Context(), state, at, time.Time{}, model.CallsReport))
		})
	}
	group.Wait()
	listed, err := store.List(t.Context(), time.Time{}, time.Time{})
	assert.NoError(t, err)
	assert.Equal(t, []model.CallTotal{{Service: "users", Environment: "production", Since: at, Start: model.TotalFirstReport, Calls: model.Calls{Other: 20}, Runs: 20}}, listed.CallTotals)
}

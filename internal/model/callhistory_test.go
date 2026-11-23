package model_test

import (
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/block/spectre/internal/model"
)

func TestCallHistoryCompactsCheckpointsWithoutLosingTotals(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	state := model.State{Service: "users", Environment: "production", Run: "a", Calls: model.Calls{Other: 3}}
	original := (model.CallHistory{}).Observed(state, at)
	state.Run, state.Calls.Other = "b", 2
	updated := original.Observed(state, at.Add(25*time.Hour))
	assert.Equal(t, model.CallTotal{Service: "users", Environment: "production", Since: at, Start: model.TotalFirstReport, Calls: model.Calls{Other: 5}, Runs: 2}, updated.Total)
	assert.Equal(t, 1, len(updated.Recent))
	assert.Equal(t, uint64(3), original.Recent["a"].Counted.Other)
}

func TestClearOverridesQuarantineClockSkew(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	state := model.State{Service: "users", Environment: "production", Run: "a"}
	//nolint:exhaustive // Only ingress quarantined.
	state.Quarantines = map[model.Direction]model.Quarantine{model.Ingress: {Since: at.Add(time.Hour)}}
	environment := (model.ServiceEnvironment{}).Observed(state, at, time.Time{}, model.StateReport)
	assert.Equal(t, at, environment.Calls.Updated)
	environment = environment.ClearedAt(at.Add(time.Minute), time.Time{})
	assert.Equal(t, model.CallTotal{Service: "users", Environment: "production", Since: at.Add(time.Minute), Start: model.TotalClear}, environment.Calls.Total)
}

func TestQuarantineWithoutRunIdentityDoesNotBlockLaterReports(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	state := model.State{Service: "users", Environment: "production"}
	//nolint:exhaustive // Only ingress quarantined.
	state.Quarantines = map[model.Direction]model.Quarantine{model.Ingress: {Since: at}}
	e := (model.ServiceEnvironment{}).Observed(state, at, time.Time{}, model.StateReport)
	state.Quarantines, state.Calls = nil, model.Calls{Other: 2}
	e = e.Observed(state, at.Add(time.Minute), time.Time{}, model.CallsReport)
	assert.Equal(t, model.Calls{Other: 2}, e.Calls.Total.Calls)
}

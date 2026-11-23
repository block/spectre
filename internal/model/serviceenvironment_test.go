package model_test

import (
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
)

func TestServiceEnvironmentPreservesSnapshots(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	//nolint:exhaustive // Only ingress quarantined.
	state := model.State{Service: "users", Environment: "production", Revision: "first",
		Quarantines: map[model.Direction]model.Quarantine{model.Ingress: {Since: at}}}
	original := (model.ServiceEnvironment{}).Observed(state, at, time.Time{}, model.StateReport)
	earlier := state.Divergences()
	state.Revision = "second"
	updated := original.Observed(state, at.Add(time.Minute), time.Time{}, model.StateReport)
	assert.Equal(t, earlier, original.Divergences)
	assert.Equal(t, state.Divergences(), updated.Divergences)
	assert.Equal(t, Some(at), updated.ClearedAt(at, time.Time{}).Cleared)
	assert.Equal(t, state.Divergences(), updated.Divergences)
}

func TestStatusEnvironmentsPreservesReports(t *testing.T) {
	first := model.State{Service: "users", Environment: "production", Revision: "first"}
	second := first
	second.Revision = "second"
	divergence := model.Divergence{Service: first.Service, Environment: first.Environment, Direction: model.Ingress}
	status := model.Status{
		States:      []model.State{first, second},
		Divergences: []model.Divergence{divergence, divergence},
	}
	assert.Equal(t, []model.EnvironmentStatus{{
		Service: "users", Environment: "production", State: Some(second),
		Divergences: []model.Divergence{divergence, divergence},
	}}, status.Environments())
	assert.Equal(t, []model.State{first, second}, status.States)
}

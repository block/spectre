package model_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
)

func TestStateFilterMatches(t *testing.T) {
	state := model.State{
		Service:         "users",
		Environment:     "production",
		CandidateCommit: "abc123",
		Quarantines: map[model.Direction]model.Quarantine{
			model.Ingress: {Reason: model.QuarantineReason{Kind: model.QuarantineDivergent, Node: Some("users")}},
			model.Egress:  {Reason: model.QuarantineReason{Kind: model.QuarantineUnmatched}},
		},
	}
	healthy := state
	healthy.Quarantines = nil
	tests := []struct {
		name    string
		filter  model.StateFilter
		state   model.State
		matches bool
	}{
		{name: "Unset", state: state, matches: true},
		{name: "UnsetMatchesHealthy", state: healthy, matches: true},
		{name: "AllFields", filter: model.StateFilter{Service: Some("users"), Environment: Some("production"), CandidateCommit: Some("abc123"), Node: Some("users")}, state: state, matches: true},
		{name: "OtherEnvironment", filter: model.StateFilter{Environment: Some("staging")}, state: state},
		{name: "OtherCandidateCommit", filter: model.StateFilter{CandidateCommit: Some("def456")}, state: state},
		{name: "OtherNode", filter: model.StateFilter{Node: Some("orders")}, state: state},
		{name: "NodeRejectsHealthy", filter: model.StateFilter{Node: Some("users")}, state: healthy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.matches, test.filter.Matches(test.state))
		})
	}
}

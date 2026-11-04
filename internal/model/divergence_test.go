package model_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
)

func TestDivergenceFilterMatchesState(t *testing.T) {
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
		filter  model.DivergenceFilter
		state   model.State
		matches bool
	}{
		{name: "Unset", state: state, matches: true},
		{name: "UnsetMatchesHealthy", state: healthy, matches: true},
		{name: "AllFields", filter: model.DivergenceFilter{Service: Some("users"), Environment: Some("production"), CandidateCommit: Some("abc123"), Node: Some("users")}, state: state, matches: true},
		{name: "OtherEnvironment", filter: model.DivergenceFilter{Environment: Some("staging")}, state: state},
		{name: "OtherCandidateCommit", filter: model.DivergenceFilter{CandidateCommit: Some("def456")}, state: state},
		{name: "OtherNode", filter: model.DivergenceFilter{Node: Some("orders")}, state: state},
		{name: "NodeRejectsHealthy", filter: model.DivergenceFilter{Node: Some("users")}, state: healthy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.matches, test.filter.MatchesState(test.state))
		})
	}
}

func TestDivergenceFilterMatches(t *testing.T) {
	divergence := model.Divergence{
		Service:         "users",
		Environment:     "production",
		CandidateCommit: "abc123",
		Direction:       model.Ingress,
		Quarantine:      model.Quarantine{Reason: model.QuarantineReason{Kind: model.QuarantineDivergent, Node: Some("users")}},
	}
	withoutNode := divergence
	withoutNode.Quarantine.Reason.Node = None[string]()
	tests := []struct {
		name       string
		filter     model.DivergenceFilter
		divergence model.Divergence
		matches    bool
	}{
		{name: "Unset", divergence: divergence, matches: true},
		{name: "AllFields", filter: model.DivergenceFilter{Service: Some("users"), Environment: Some("production"), CandidateCommit: Some("abc123"), Node: Some("users")}, divergence: divergence, matches: true},
		{name: "OtherService", filter: model.DivergenceFilter{Service: Some("orders")}, divergence: divergence},
		{name: "OtherCandidateCommit", filter: model.DivergenceFilter{CandidateCommit: Some("def456")}, divergence: divergence},
		{name: "OtherNode", filter: model.DivergenceFilter{Node: Some("orders")}, divergence: divergence},
		{name: "NodeRejectsMissingNode", filter: model.DivergenceFilter{Node: Some("users")}, divergence: withoutNode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.matches, test.filter.Matches(test.divergence))
		})
	}
}

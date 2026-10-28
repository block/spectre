package model_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
)

func TestDivergenceFilterMatches(t *testing.T) {
	divergence := model.Divergence{
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
		Direction:   model.Ingress,
		Quarantine:  model.Quarantine{Reason: model.QuarantineReason{Kind: model.QuarantineDivergent, Node: Some("users")}},
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
		{name: "UnsetMatchesAbsentNode", divergence: withoutNode, matches: true},
		{name: "AllFields", filter: model.DivergenceFilter{Service: Some("users"), Environment: Some("production"), Revision: Some("abc123"), Node: Some("users")}, divergence: divergence, matches: true},
		{name: "OtherEnvironment", filter: model.DivergenceFilter{Environment: Some("staging")}, divergence: divergence},
		{name: "OtherRevision", filter: model.DivergenceFilter{Revision: Some("def456")}, divergence: divergence},
		{name: "NodeRejectsAbsentNode", filter: model.DivergenceFilter{Node: Some("users")}, divergence: withoutNode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.matches, test.filter.Matches(test.divergence))
		})
	}
}

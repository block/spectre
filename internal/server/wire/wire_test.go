package wire_test

import (
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/wire"
)

func TestDivergenceRoundTrip(t *testing.T) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		divergence model.Divergence
	}{
		{name: "Divergent", divergence: model.Divergence{
			Service:     "users",
			Environment: "production",
			Revision:    "abc123",
			Direction:   model.Ingress,
			Quarantine: model.Quarantine{Since: since, Reason: model.QuarantineReason{
				Kind:        model.QuarantineDivergent,
				Traffic:     Some(model.Traffic{Direction: model.Ingress, Protocol: model.ProtocolHTTP, Route: "GET /v1/users"}),
				Node:        Some("users"),
				Differences: []string{"body.name"},
			}},
		}},
		{name: "WithoutTraffic", divergence: model.Divergence{
			Service:     "users",
			Environment: "staging",
			Revision:    "abc123",
			Direction:   model.Egress,
			Quarantine: model.Quarantine{Since: since, Reason: model.QuarantineReason{
				Kind:   model.QuarantineCapacity,
				Detail: "candidate concurrency limit exceeded",
			}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			restored, err := wire.DivergenceFromMessage(wire.DivergenceMessage(test.divergence))
			assert.NoError(t, err)
			assert.Equal(t, test.divergence, restored)
		})
	}
}

func TestFilterRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		filter model.DivergenceFilter
	}{
		{name: "Unset"},
		{name: "Set", filter: model.DivergenceFilter{Service: Some("users"), Environment: Some("production"), Revision: Some("abc123"), Node: Some("users")}},
		// An empty filter value is set, and matches only empty values.
		{name: "Empty", filter: model.DivergenceFilter{Node: Some("")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.filter, wire.FilterFromMessage(wire.FilterMessage(test.filter)))
		})
	}
}

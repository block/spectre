package cli

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/storage"
)

func TestDivergencesListCommand(t *testing.T) {
	httpServer := httptest.NewServer(server.NewService(storage.NewMemory()).Handler())
	t.Cleanup(httpServer.Close)
	config := client.Config{URL: httpServer.URL}
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	divergent := model.Divergence{
		Service:     "users",
		Environment: "production",
		Revision:    "abc123",
		Direction:   model.Ingress,
		Quarantine: model.Quarantine{Since: first.Add(time.Minute), Reason: model.QuarantineReason{
			Kind:        model.QuarantineDivergent,
			Traffic:     Some(model.Traffic{Direction: model.Ingress, Protocol: model.ProtocolHTTP, Route: "GET /v1/users"}),
			Node:        Some("users"),
			Differences: []string{"body.name"},
		}},
	}
	unmatched := model.Divergence{
		Service:     "users",
		Environment: "staging",
		Revision:    "abc123",
		Direction:   model.Egress,
		Quarantine: model.Quarantine{Since: first, Reason: model.QuarantineReason{
			Kind:   model.QuarantineUnmatched,
			Detail: "no matching reference call",
		}},
	}
	for _, divergence := range []model.Divergence{divergent, unmatched} {
		assert.NoError(t, client.New(config).Quarantine(t.Context(), divergence))
	}

	var text strings.Builder
	command := divergencesListCommand{Client: config}
	assert.NoError(t, command.Run(t.Context(), Output{Writer: &text}))
	assert.Equal(t, `users abc123 production
  ingress http GET /v1/users: response differences: body.name (node users)
  quarantined 2026-10-01T12:01:00Z
users abc123 staging
  egress: no matching reference call
  quarantined 2026-10-01T12:00:00Z
`, text.String())

	var data strings.Builder
	command.JSON = true
	command.Node = "users"
	assert.NoError(t, command.Run(t.Context(), Output{Writer: &data}))
	// Protobuf JSON output varies its whitespace, so compare decoded values.
	var expected, printed any
	assert.NoError(t, json.Unmarshal([]byte(`{"divergences": [{
		"service": "users",
		"environment": "production",
		"revision": "abc123",
		"direction": "DIRECTION_INGRESS",
		"quarantine": {
			"since": "2026-10-01T12:01:00Z",
			"kind": "KIND_DIVERGENT",
			"traffic": {"direction": "DIRECTION_INGRESS", "protocol": "http", "route": "GET /v1/users"},
			"node": "users",
			"differences": ["body.name"]
		}
	}]}`), &expected))
	assert.NoError(t, json.Unmarshal([]byte(data.String()), &printed))
	assert.Equal(t, expected, printed)
}

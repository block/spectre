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

func TestDivergencesCommand(t *testing.T) {
	httpServer := httptest.NewServer(server.NewService(storage.NewMemory(), time.Hour).Handler())
	t.Cleanup(httpServer.Close)
	config := client.Config{URL: httpServer.URL}
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	quarantined := model.State{
		Service:         "users",
		Environment:     "production",
		CandidateCommit: "abc123",
		SpectreVersion:  "v1.2.3",
		Uptime:          90 * time.Minute,
		Quarantines: map[model.Direction]model.Quarantine{
			model.Ingress: {Since: first.Add(time.Minute), Reason: model.QuarantineReason{
				Kind:        model.QuarantineDivergent,
				Traffic:     Some(model.Traffic{Direction: model.Ingress, Protocol: model.ProtocolHTTP, Route: "GET /v1/users"}),
				Node:        Some("users"),
				Differences: []string{"body.name"},
			}},
			model.Egress: {Since: first, Reason: model.QuarantineReason{
				Kind:   model.QuarantineUnmatched,
				Detail: "no matching reference call",
			}},
		},
	}
	healthy := model.State{
		Service:         "users",
		Environment:     "staging",
		CandidateCommit: "def456",
		SpectreVersion:  "v1.2.3",
		Uptime:          12*time.Minute + 300*time.Millisecond,
	}
	for _, state := range []model.State{quarantined, healthy} {
		assert.NoError(t, client.New(config).Heartbeat(t.Context(), state))
	}

	var text strings.Builder
	command := divergencesCommand{Client: config}
	assert.NoError(t, command.Run(t.Context(), Output{Writer: &text}))
	assert.Equal(t, `users abc123 production spectre v1.2.3, up 1h30m0s
  ingress http GET /v1/users: response differences: body.name (node users)
  quarantined 2026-10-01T12:01:00Z
  egress: no matching reference call
  quarantined 2026-10-01T12:00:00Z
users def456 staging spectre v1.2.3, up 12m0s
  healthy
`, text.String())

	var data strings.Builder
	command.JSON = true
	command.CandidateCommit = "def456"
	assert.NoError(t, command.Run(t.Context(), Output{Writer: &data}))
	// Protobuf JSON output varies its whitespace, so compare decoded values.
	var expected, printed any
	assert.NoError(t, json.Unmarshal([]byte(`{"states": [{
		"service": "users",
		"environment": "staging",
		"candidateCommit": "def456",
		"spectreVersion": "v1.2.3",
		"uptime": "720.300s"
	}]}`), &expected))
	assert.NoError(t, json.Unmarshal([]byte(data.String()), &printed))
	assert.Equal(t, expected, printed)

	var empty strings.Builder
	command = divergencesCommand{Client: config, Service: "invoices"}
	assert.NoError(t, command.Run(t.Context(), Output{Writer: &empty}))
	assert.Equal(t, "No mirrors are reporting.\n", empty.String())
}

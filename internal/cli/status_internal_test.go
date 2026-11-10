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

func TestStatusCommands(t *testing.T) {
	store := storage.NewMemory()
	httpServer := httptest.NewServer(server.NewService(store, time.Hour).Handler())
	t.Cleanup(httpServer.Close)
	spectre := client.NewServers(client.ServersConfig{URLs: []string{httpServer.URL}})
	first := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	unmatched := model.QuarantineReason{Kind: model.QuarantineUnmatched, Detail: "no matching reference call"}
	// Divergences from mirror runs that have since stopped or restarted.
	for _, earlier := range []model.Divergence{
		{Service: "users", Environment: "canary", Revision: "abc123", Direction: model.Egress, Quarantine: model.Quarantine{Since: first, Reason: unmatched}},
		{Service: "users", Environment: "production", Revision: "abc123", Direction: model.Egress, Quarantine: model.Quarantine{Since: first, Reason: unmatched}},
	} {
		assert.NoError(t, store.Record(t.Context(), earlier))
	}
	quarantined := model.State{
		Service:        "users",
		Environment:    "production",
		Revision:       "def456",
		SpectreVersion: "v1.2.3",
		Uptime:         90 * time.Minute,
		//nolint:exhaustive // Only the ingress proxy quarantined in this run.
		Quarantines: map[model.Direction]model.Quarantine{
			model.Ingress: {Since: first.Add(time.Minute), Reason: model.QuarantineReason{
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
			}},
		},
		Calls: model.Calls{Routes: map[model.Route]uint64{"GET /v1/users": 1}},
	}
	healthy := model.State{
		Service:        "users",
		Environment:    "staging",
		Revision:       "def456",
		SpectreVersion: "v1.2.3",
		Uptime:         12*time.Minute + 300*time.Millisecond,
		Calls:          model.Calls{Routes: map[model.Route]uint64{"POST /v1/users": 66, "GET /v1/users": 22}, Other: 3},
	}
	for _, state := range []model.State{quarantined, healthy} {
		assert.NoError(t, store.Put(t.Context(), state, time.Now()))
	}
	assert.NoError(t, store.Record(t.Context(), model.Divergence{Service: "users", Environment: "production", Revision: "def456", Direction: model.Ingress, Quarantine: quarantined.Quarantines[model.Ingress]}))

	var text strings.Builder
	list := statusListCommand{}
	assert.NoError(t, list.Run(t.Context(), spectre, Output{Writer: &text}))
	assert.Equal(t, `users canary not reporting
  egress: no matching reference call
  quarantined 2026-10-01T12:00:00Z on abc123
users def456 production spectre v1.2.3, up 1h30m0s
  ingress http GET /v1/users: response differences (node users)
    ~ $.name
    - $.emails[*] ×2
    ! $.owner object → null
    ~ $status 200 → 500
    … more
  quarantined 2026-10-01T12:01:00Z
    GET /v1/users: 1 call
  egress: no matching reference call
  quarantined 2026-10-01T12:00:00Z on abc123, in an earlier run
users def456 staging spectre v1.2.3, up 12m0s
  healthy
    GET /v1/users: 22 calls
    POST /v1/users: 66 calls
    other routes: 3 calls
`, text.String())

	var data strings.Builder
	list.JSON = true
	list.Environment = "production"
	list.Revision = "def456"
	assert.NoError(t, list.Run(t.Context(), spectre, Output{Writer: &data}))
	// Protobuf JSON output varies its whitespace, so compare decoded values.
	quarantine := `{
		"direction": "DIRECTION_INGRESS",
		"since": "2026-10-01T12:01:00Z",
		"kind": "KIND_DIVERGENT",
		"traffic": {"direction": "DIRECTION_INGRESS", "protocol": "http", "route": "GET /v1/users"},
		"node": "users",
		"differences": [
			{"path": "$.name", "change": "CHANGE_CHANGED", "count": 1},
			{"path": "$.emails[*]", "change": "CHANGE_MISSING", "count": 2},
			{"path": "$.owner", "change": "CHANGE_RETYPED", "referenceType": "JSON_TYPE_OBJECT", "candidateType": "JSON_TYPE_NULL", "count": 1},
			{"path": "$status", "change": "CHANGE_CHANGED", "statuses": {"reference": "200", "candidate": "500"}, "count": 1}
		],
		"truncated": true
	}`
	var expected, printed any
	assert.NoError(t, json.Unmarshal([]byte(`{
		"states": [{
			"service": "users",
			"environment": "production",
			"revision": "def456",
			"spectreVersion": "v1.2.3",
			"uptime": "5400s",
			"quarantines": [`+quarantine+`],
			"calls": {"routes": {"GET /v1/users": "1"}}
		}],
		"divergences": [{
			"service": "users",
			"environment": "production",
			"revision": "def456",
			"quarantine": `+quarantine+`
		}]
	}`), &expected))
	assert.NoError(t, json.Unmarshal([]byte(data.String()), &printed))
	assert.Equal(t, expected, printed)

	clearCommand := statusClearCommand{Clear: server.ClearConfig{Service: "users", Environment: "production"}}
	assert.NoError(t, clearCommand.Run(t.Context(), spectre))
	text.Reset()
	list = statusListCommand{Environment: "production"}
	assert.NoError(t, list.Run(t.Context(), spectre, Output{Writer: &text}))
	// The mirror still holds its quarantine, so it lists until the mirror restarts.
	assert.Equal(t, `users def456 production spectre v1.2.3, up 1h30m0s
  ingress http GET /v1/users: response differences (node users)
    ~ $.name
    - $.emails[*] ×2
    ! $.owner object → null
    ~ $status 200 → 500
    … more
  quarantined 2026-10-01T12:01:00Z
    GET /v1/users: 1 call
`, text.String())

	text.Reset()
	list = statusListCommand{Service: "invoices"}
	assert.NoError(t, list.Run(t.Context(), spectre, Output{Writer: &text}))
	assert.Equal(t, "No mirrors or divergences.\n", text.String())
}

func TestStatusCallsEveryServer(t *testing.T) {
	newServer := func(state model.State) (*storage.Memory, string) {
		store := storage.NewMemory()
		assert.NoError(t, store.Put(t.Context(), state, time.Now()))
		httpServer := httptest.NewServer(server.NewService(store, time.Hour).Handler())
		t.Cleanup(httpServer.Close)
		return store, httpServer.URL
	}
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	quarantine := model.Quarantine{Since: since, Reason: model.QuarantineReason{Kind: model.QuarantineUnmatched, Detail: "no matching reference call"}}
	production := model.State{Service: "users", Environment: "production", Revision: "def456", SpectreVersion: "v1.2.3", Uptime: time.Minute}
	staging := model.State{Service: "users", Environment: "staging", Revision: "def456", SpectreVersion: "v1.2.3", Uptime: time.Hour}
	productionStore, productionURL := newServer(production)
	stagingStore, stagingURL := newServer(staging)
	assert.NoError(t, productionStore.Record(t.Context(), model.Divergence{Service: "users", Environment: "production", Revision: "abc123", Direction: model.Egress, Quarantine: quarantine}))
	unreachable := httptest.NewServer(nil)
	unreachable.Close()

	// The production server comes first, but the output is ordered as if one
	// server listed it all.
	servers := client.NewServers(client.ServersConfig{URLs: []string{productionURL, stagingURL, productionURL}})
	var text strings.Builder
	list := statusListCommand{}
	assert.NoError(t, list.Run(t.Context(), servers, Output{Writer: &text}))
	expected := `users def456 production spectre v1.2.3, up 1m0s
  healthy
  egress: no matching reference call
  quarantined 2026-10-01T12:00:00Z on abc123, in an earlier run
users def456 staging spectre v1.2.3, up 1h0m0s
  healthy
`
	assert.Equal(t, expected, text.String())

	// A server that fails does not hide what the others list.
	servers = client.NewServers(client.ServersConfig{URLs: []string{unreachable.URL, productionURL, stagingURL}})
	text.Reset()
	err := list.Run(t.Context(), servers, Output{Writer: &text})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), unreachable.URL+": list divergences")
	assert.Equal(t, expected, text.String())

	servers = client.NewServers(client.ServersConfig{URLs: []string{productionURL, stagingURL}})
	clearCommand := statusClearCommand{Clear: server.ClearConfig{Service: "users", Environment: "production"}}
	assert.NoError(t, clearCommand.Run(t.Context(), servers))
	for _, store := range []*storage.Memory{productionStore, stagingStore} {
		divergences, err := store.Divergences(t.Context())
		assert.NoError(t, err)
		assert.Equal(t, 0, len(divergences))
	}
}

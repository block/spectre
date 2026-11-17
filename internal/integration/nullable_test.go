//go:build integration

package integration_test

import (
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/grpcreflect/v2"
	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
)

func TestNullMembersAreCompared(t *testing.T) {
	t.Parallel()
	m := startFeeMirror(t, `{"fee_plan_id":null,"note":null}`, `{"fee_plan_id":null,"note":null}`)

	status, body := m.get(t, m.ingressURL+"/fee")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, `{"fee_plan_id":null,"note":null}`, body)
	state := m.waitForCalls(t, map[model.Route]uint64{"GET /fee": 1})
	assert.Equal(t, map[model.Direction]model.Quarantine(nil), state.Quarantines)
	assert.Equal(t, model.Calls{Routes: map[model.Route]uint64{"GET /fee": 1}}, state.Calls)
}

func TestNullAgainstStringQuarantinesCandidate(t *testing.T) {
	t.Parallel()
	m := startFeeMirror(t, `{"fee_plan_id":null}`, `{"fee_plan_id":"plan-1"}`)

	status, _ := m.get(t, m.ingressURL+"/fee")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, model.QuarantineReason{
		Kind:    model.QuarantineDivergent,
		Traffic: Some(model.Traffic{Direction: model.Ingress, Protocol: model.ProtocolHTTP, Route: "GET /fee"}),
		Differences: []model.Difference{{
			Path:   "$.fee_plan_id",
			Change: model.ChangeRetyped,
			Retype: Some(model.Retype{Reference: model.JSONNull, Candidate: model.JSONString}),
			Count:  1,
		}},
	}, m.waitForQuarantine(t, model.Ingress))
}

// startFeeMirror mirrors a service whose reference and candidate answer GET /fee with the given bodies.
func startFeeMirror(t *testing.T, reference string, candidate string) *running {
	t.Helper()
	respond := func(body string) service {
		return func(egress) http.Handler {
			// The proxy loads gRPC reflection before it is ready.
			server := connect.NewServer()
			grpcreflect.Register(server)
			mux := http.NewServeMux()
			connecthttp.Mount(mux, server)
			mux.HandleFunc("GET /fee", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(body))
				assert.NoError(t, err)
			})
			return mux
		}
	}
	return start(t, mirror{
		reference:  respond(reference),
		candidate:  respond(candidate),
		scriptsDir: "testdata/nullable/scripts",
		schemaDir:  "testdata/nullable/schema",
	})
}

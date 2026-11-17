//go:build integration

package integration_test

import (
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/integration/it"
	"github.com/block/spectre/internal/model"
)

func TestNullMembersAreCompared(t *testing.T) {
	m := startFeeMirror(t, `{"fee_plan_id":null,"note":null}`, `{"fee_plan_id":null,"note":null}`)

	m.Ingress.Get("/fee").ExpectStatus(http.StatusOK).ExpectBodyContains(`"fee_plan_id":null`)
	m.ExpectCalls(it.Calls{"GET /fee": 1})
}

func TestNullAgainstStringQuarantinesCandidate(t *testing.T) {
	m := startFeeMirror(t, `{"fee_plan_id":null}`, `{"fee_plan_id":"plan-1"}`)

	m.Ingress.Get("/fee").ExpectStatus(http.StatusOK)
	m.ExpectQuarantine(it.DivergentIngress("GET /fee", model.Difference{
		Path:   "$.fee_plan_id",
		Change: model.ChangeRetyped,
		Retype: Some(model.Retype{Reference: model.JSONNull, Candidate: model.JSONString}),
		Count:  1,
	}))
}

// startFeeMirror mirrors services that answer GET /fee with the given bodies,
// compared as a fee whose members may be null.
func startFeeMirror(t *testing.T, reference, candidate string) *it.Mirror {
	t.Helper()
	return it.Start(t,
		it.Schema(`
			declare module "fees" {
			  export interface Fee {
			    fee_plan_id: string | null;
			    note?: string | null;
			  }
			}
		`),
		it.Scripts(`
			import { ingress } from "spectre";
			import type { Fee } from "fees";

			ingress.http("GET /fee").match<Fee>();
		`),
		it.Reference(feeService(reference)),
		it.Candidate(feeService(candidate)),
	)
}

// feeService answers GET /fee with body.
func feeService(body string) it.Service {
	return func(t *testing.T, backend it.Backend) {
		backend.HTTP.HandleFunc("GET /fee", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(body))
			assert.NoError(t, err)
		})
	}
}

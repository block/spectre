package event_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/event"
	"github.com/block/spectre/internal/protocols/event/script"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/schema"
	"github.com/block/spectre/internal/state"
	"github.com/block/spectre/spectrepb/eventpb"
	"github.com/block/spectre/spectrepb/eventpb/eventpbconnect"
)

const proactSchema = `declare module "proact" {
  export interface VerifySiretWorker {
    merchant_token: string;
    legal_entity_token: string;
    merchant_exists: boolean;
  }
  export interface VerifySiretJob {
    merchantToken: string;
    legalEntityToken: string;
    merchantExists: boolean;
  }
  export interface SidekiqPush {
    jid: string;
  }
  export interface EnqueuedJob {
    jobId: string;
  }
}
`

const proactScript = `import * as spectre from "spectre";
import type { EnqueuedJob, SidekiqPush, VerifySiretJob, VerifySiretWorker } from "proact";

const toJob = (args: VerifySiretWorker): VerifySiretJob => ({
  merchantToken: args.merchant_token,
  legalEntityToken: args.legal_entity_token,
  merchantExists: args.merchant_exists,
});

spectre.ingress.event("sidekiq/perform/VerifySiretWorker")
  .triggers("jsc/handle/VerifySiretJob")
  .payload<VerifySiretWorker, VerifySiretJob>(toJob);
spectre.ingress.event("cron/refresh-rates").triggers();

spectre.egress.event("sidekiq/push/VerifySiretWorker")
  .pairs("jsc/enqueue/VerifySiretJob")
  .payload<VerifySiretWorker, VerifySiretJob>(toJob)
  .result<SidekiqPush, EnqueuedJob>((pushed) => ({ jobId: pushed.jid }));
spectre.egress.event("audit/record").optional().match<SidekiqPush>();
`

const (
	workerArgs = `{"merchant_token":"MT1","legal_entity_token":"LE1","merchant_exists":true}`
	jobArgs    = `{"merchantToken":"MT1","legalEntityToken":"LE1","merchantExists":true}`
)

// mirror is an event server on shared machinery that the test owns, as the mirror does.
type mirror struct {
	reference         eventpbconnect.EventServiceClient
	candidate         eventpbconnect.EventServiceClient
	ingressCandidates *proxy.Candidates
	egressCandidates  *proxy.Candidates
}

func newMirror(t *testing.T, nodes Option[[]state.KindNode]) *mirror {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	scripts, schemaDir := t.TempDir(), t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(scripts, "events.ts"), []byte(proactScript), 0o600))
	assert.NoError(t, os.WriteFile(filepath.Join(schemaDir, "proact.d.ts"), []byte(proactSchema), 0o600))
	config := engine.NewConfig()
	config.Comparison.ScriptsDir = scripts
	config.Comparison.Schema.SchemaDir = schemaDir
	config.MatchWindow = 200 * time.Millisecond
	ingressCandidates := proxy.NewCandidates(proxy.NewCandidatesConfig())
	egressCandidates := proxy.NewCandidates(proxy.NewCandidatesConfig())
	provider := event.NewProvider()
	pairing, err := engine.NewEngine(ctx, config, []javascript.Binding{provider.Scripts()}, ingressCandidates, egressCandidates, None[engine.CallCounter]())
	assert.NoError(t, err)
	noSource := func(model.Route) Option[schema.TypeRef] { return None[schema.TypeRef]() }
	runtime := protocols.Runtime{
		Schema:  pairing.Schema(),
		Scripts: pairing.Scripts(),
		Ingress: Some(protocols.Side{
			Reference: pairing.IngressReference(script.Protocol), Candidate: pairing.IngressCandidate(script.Protocol),
			Candidates: ingressCandidates, Quarantiner: ingressCandidates,
			Health: health.New(http.NotFoundHandler()), Nodes: nodes, SourceResponse: noSource,
		}),
		Egress: Some(protocols.Side{
			Reference: pairing.EgressReference(script.Protocol), Candidate: pairing.EgressCandidate(script.Protocol),
			Candidates: egressCandidates, Quarantiner: egressCandidates,
			Health: health.New(http.NotFoundHandler()), SourceResponse: noSource,
		}),
	}
	eventConfig := event.NewConfig()
	eventConfig.ReferenceListen = "127.0.0.1:0"
	server, err := provider.New(ctx, eventConfig, runtime)
	assert.NoError(t, err)
	referenceListener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	candidateListener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	served := make(chan error, 1)
	go func() {
		served <- server.Serve(ctx, map[string]net.Listener{event.ReferenceListener: referenceListener, event.CandidateListener: candidateListener})
	}()
	t.Cleanup(func() {
		// Stop candidate work before the server, as the mirror does.
		pairing.Close()
		ingressCandidates.Close()
		egressCandidates.Close()
		cancel()
		assert.NoError(t, <-served)
		assert.NoError(t, pairing.Shutdown(context.Background()))
	})
	return &mirror{
		reference:         client(referenceListener),
		candidate:         client(candidateListener),
		ingressCandidates: ingressCandidates,
		egressCandidates:  egressCandidates,
	}
}

func client(listener net.Listener) eventpbconnect.EventServiceClient {
	return eventpbconnect.NewEventServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, "http://"+listener.Addr().String())))
}

// receive opens the candidate's stream, and returns the runs it receives.
func (m *mirror) receive(t *testing.T) <-chan *eventpb.ReceiveResponse {
	t.Helper()
	stream, err := m.candidate.Receive(t.Context(), &eventpb.ReceiveRequest{})
	assert.NoError(t, err)
	runs := make(chan *eventpb.ReceiveResponse, 8)
	go func() {
		defer close(runs)
		for {
			run, err := stream.Receive()
			if err != nil {
				return
			}
			runs <- run
		}
	}()
	return runs
}

// start reports a start event until it is shadowed, because the candidate's stream may not
// be open yet, and returns its run ID.
func (m *mirror) start(t *testing.T, eventType string, payload string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := m.reference.Report(t.Context(), &eventpb.ReportRequest{Event: &eventpb.Event{Type: eventType, Payload: []byte(payload)}})
		assert.NoError(t, err)
		if response.RunId != nil {
			return response.GetRunId()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("start event was never shadowed")
	return ""
}

func next(t *testing.T, runs <-chan *eventpb.ReceiveResponse) *eventpb.ReceiveResponse {
	t.Helper()
	select {
	case run := <-runs:
		return run
	case <-time.After(5 * time.Second):
		t.Fatal("candidate received no run")
		return nil //nolint:optionalnil // unreachable after t.Fatal
	}
}

// quarantine waits for either direction's quarantine.
func (m *mirror) quarantine(t *testing.T) model.Quarantine {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, candidates := range []*proxy.Candidates{m.ingressCandidates, m.egressCandidates} {
			if quarantine, ok := candidates.QuarantineStatus().Get(); ok {
				return quarantine
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("candidate was never quarantined")
	return model.Quarantine{}
}

func (m *mirror) assertNotQuarantined(t *testing.T) {
	t.Helper()
	assert.False(t, m.ingressCandidates.QuarantineStatus().Ok())
	assert.False(t, m.egressCandidates.QuarantineStatus().Ok())
}

// assertJSON compares JSON by value, because encoding sorts object keys.
func assertJSON(t *testing.T, expected string, actual []byte) {
	t.Helper()
	var expectedValue, actualValue map[string]any
	assert.NoError(t, json.Unmarshal([]byte(expected), &expectedValue))
	assert.NoError(t, json.Unmarshal(actual, &actualValue))
	assert.Equal(t, expectedValue, actualValue)
}

func TestStartTriggersCandidateWithMappedPayload(t *testing.T) {
	m := newMirror(t, None[[]state.KindNode]())
	runs := m.receive(t)
	runID := m.start(t, "sidekiq/perform/VerifySiretWorker", workerArgs)
	run := next(t, runs)
	assert.Equal(t, runID, run.GetRunId())
	assert.Equal(t, "jsc/handle/VerifySiretJob", run.GetEvent().GetType())
	assertJSON(t, jobArgs, run.GetEvent().GetPayload())
}

func TestStartWithoutPayloadMappingPassesPayloadUnchanged(t *testing.T) {
	m := newMirror(t, None[[]state.KindNode]())
	runs := m.receive(t)
	runID := m.start(t, "cron/refresh-rates", `{"at":"2026-11-24T00:00:00Z"}`)
	run := next(t, runs)
	assert.Equal(t, runID, run.GetRunId())
	assert.Equal(t, "cron/refresh-rates", run.GetEvent().GetType())
	assert.Equal(t, `{"at":"2026-11-24T00:00:00Z"}`, string(run.GetEvent().GetPayload()))
}

func TestStartIsNotShadowedWithoutCandidateStream(t *testing.T) {
	m := newMirror(t, None[[]state.KindNode]())
	response, err := m.reference.Report(t.Context(), &eventpb.ReportRequest{Event: &eventpb.Event{Type: "cron/refresh-rates"}})
	assert.NoError(t, err)
	assert.Zero(t, response.RunId)
}

func TestProducedEventPairsAndReturnsMappedResult(t *testing.T) {
	m := newMirror(t, None[[]state.KindNode]())
	runs := m.receive(t)
	runID := m.start(t, "sidekiq/perform/VerifySiretWorker", workerArgs)
	next(t, runs)
	_, err := m.reference.Report(t.Context(), &eventpb.ReportRequest{
		Event:  &eventpb.Event{Type: "sidekiq/push/VerifySiretWorker", Payload: []byte(workerArgs)},
		RunId:  &runID,
		Result: &eventpb.Result{Result: &eventpb.Result_Message{Message: []byte(`{"jid":"J1"}`)}},
	})
	assert.NoError(t, err)
	response, err := m.candidate.Report(t.Context(), &eventpb.ReportRequest{
		Event: &eventpb.Event{Type: "jsc/enqueue/VerifySiretJob", Payload: []byte(jobArgs)},
		RunId: &runID,
	})
	assert.NoError(t, err)
	assertJSON(t, `{"jobId":"J1"}`, response.GetResult().GetMessage())
	m.assertNotQuarantined(t)
}

func TestProducedEventPassesReferenceFailureUnchanged(t *testing.T) {
	m := newMirror(t, None[[]state.KindNode]())
	runs := m.receive(t)
	runID := m.start(t, "sidekiq/perform/VerifySiretWorker", workerArgs)
	next(t, runs)
	_, err := m.reference.Report(t.Context(), &eventpb.ReportRequest{
		Event:  &eventpb.Event{Type: "sidekiq/push/VerifySiretWorker", Payload: []byte(workerArgs)},
		RunId:  &runID,
		Result: &eventpb.Result{Result: &eventpb.Result_Failure{Failure: "Redis is down"}},
	})
	assert.NoError(t, err)
	response, err := m.candidate.Report(t.Context(), &eventpb.ReportRequest{
		Event: &eventpb.Event{Type: "jsc/enqueue/VerifySiretJob", Payload: []byte(jobArgs)},
		RunId: &runID,
	})
	assert.NoError(t, err)
	assert.Equal(t, "Redis is down", response.GetResult().GetFailure())
	m.assertNotQuarantined(t)
}

func TestQuarantine(t *testing.T) {
	tests := []struct {
		name string
		// run reports the events of one shadowed run, after the candidate received it.
		run      func(t *testing.T, m *mirror, runID string)
		expected model.QuarantineReason
	}{
		{
			name: "CandidatePayloadDiffers",
			run: func(t *testing.T, m *mirror, runID string) {
				t.Helper()
				_, err := m.reference.Report(t.Context(), &eventpb.ReportRequest{
					Event: &eventpb.Event{Type: "sidekiq/push/VerifySiretWorker", Payload: []byte(workerArgs)},
					RunId: &runID,
				})
				assert.NoError(t, err)
				_, err = m.candidate.Report(t.Context(), &eventpb.ReportRequest{
					Event: &eventpb.Event{Type: "jsc/enqueue/VerifySiretJob", Payload: []byte(`{"merchantToken":"MT1","legalEntityToken":"LE1","merchantExists":false}`)},
					RunId: &runID,
				})
				assert.Error(t, err)
			},
			expected: model.QuarantineReason{
				Kind:        model.QuarantineDivergent,
				Traffic:     Some(model.Traffic{Direction: model.Egress, Protocol: script.Protocol, Route: "jsc/enqueue/VerifySiretJob"}),
				Differences: []model.Difference{{Change: model.ChangeChanged, Count: 1}},
			},
		},
		{
			name: "CandidateLeavesOutReferenceEvent",
			run: func(t *testing.T, m *mirror, runID string) {
				t.Helper()
				_, err := m.reference.Report(t.Context(), &eventpb.ReportRequest{
					Event: &eventpb.Event{Type: "sidekiq/push/VerifySiretWorker", Payload: []byte(workerArgs)},
					RunId: &runID,
				})
				assert.NoError(t, err)
			},
			expected: model.QuarantineReason{
				Kind:    model.QuarantineUnmatched,
				Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: script.Protocol, Route: "jsc/enqueue/VerifySiretJob"}),
			},
		},
		{
			name: "CandidateReportsStartEvent",
			run: func(t *testing.T, m *mirror, runID string) {
				t.Helper()
				_, err := m.candidate.Report(t.Context(), &eventpb.ReportRequest{
					Event: &eventpb.Event{Type: "jsc/handle/VerifySiretJob", Payload: []byte(jobArgs)},
					RunId: &runID,
				})
				assert.Error(t, err)
			},
			expected: model.QuarantineReason{
				Kind:    model.QuarantineUnmatched,
				Traffic: Some(model.Traffic{Direction: model.Ingress, Protocol: script.Protocol, Route: "jsc/handle/VerifySiretJob"}),
			},
		},
		{
			name: "CandidateEventOutsideRun",
			run: func(t *testing.T, m *mirror, _ string) {
				t.Helper()
				_, err := m.candidate.Report(t.Context(), &eventpb.ReportRequest{
					Event: &eventpb.Event{Type: "jsc/enqueue/VerifySiretJob", Payload: []byte(jobArgs)},
				})
				assert.Error(t, err)
			},
			expected: model.QuarantineReason{
				Kind:    model.QuarantinePairing,
				Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: script.Protocol, Route: "jsc/enqueue/VerifySiretJob"}),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := newMirror(t, None[[]state.KindNode]())
			runs := m.receive(t)
			runID := m.start(t, "sidekiq/perform/VerifySiretWorker", workerArgs)
			next(t, runs)
			test.run(t, m, runID)
			assert.Equal(t, test.expected, m.quarantine(t).Reason, assert.Exclude[string]())
		})
	}
}

func TestOptionalReferenceEventMayBeLeftOut(t *testing.T) {
	m := newMirror(t, None[[]state.KindNode]())
	runs := m.receive(t)
	runID := m.start(t, "cron/refresh-rates", "")
	next(t, runs)
	_, err := m.reference.Report(t.Context(), &eventpb.ReportRequest{
		Event: &eventpb.Event{Type: "audit/record", Payload: []byte(`{"jid":"J1"}`)},
		RunId: &runID,
	})
	assert.NoError(t, err)
	time.Sleep(400 * time.Millisecond)
	m.assertNotQuarantined(t)
}

func TestReferenceEventOutsideRunIsNotPaired(t *testing.T) {
	m := newMirror(t, None[[]state.KindNode]())
	_, err := m.reference.Report(t.Context(), &eventpb.ReportRequest{
		Event: &eventpb.Event{Type: "sidekiq/push/VerifySiretWorker", Payload: []byte(workerArgs)},
	})
	assert.NoError(t, err)
	time.Sleep(400 * time.Millisecond)
	m.assertNotQuarantined(t)
}

func TestMigrationState(t *testing.T) {
	nodes := Some([]state.KindNode{
		{ID: "verify-siret", Migrated: true, Settings: state.Settings{"types": {"sidekiq/perform/VerifySiretWorker": {}}}},
		{ID: "refresh-rates", Migrated: false, Settings: state.Settings{"types": {"cron/refresh-rates": {}}}},
	})
	t.Run("MigratedTypeIsShadowed", func(t *testing.T) {
		m := newMirror(t, nodes)
		runs := m.receive(t)
		m.start(t, "sidekiq/perform/VerifySiretWorker", workerArgs)
		next(t, runs)
	})
	t.Run("PendingTypeIsNotShadowed", func(t *testing.T) {
		m := newMirror(t, nodes)
		m.receive(t)
		m.start(t, "sidekiq/perform/VerifySiretWorker", workerArgs) // The stream is open.
		response, err := m.reference.Report(t.Context(), &eventpb.ReportRequest{Event: &eventpb.Event{Type: "cron/refresh-rates"}})
		assert.NoError(t, err)
		assert.Zero(t, response.RunId)
		m.assertNotQuarantined(t)
	})
	t.Run("UnlistedTypeQuarantines", func(t *testing.T) {
		m := newMirror(t, Some([]state.KindNode{}))
		m.receive(t)
		response, err := m.reference.Report(t.Context(), &eventpb.ReportRequest{Event: &eventpb.Event{Type: "cron/refresh-rates"}})
		assert.NoError(t, err)
		assert.Zero(t, response.RunId)
		assert.Equal(t, model.QuarantineReason{
			Kind:    model.QuarantineUnlisted,
			Traffic: Some(model.Traffic{Direction: model.Ingress, Protocol: script.Protocol, Route: "cron/refresh-rates"}),
			Detail:  "event type cron/refresh-rates is in no node of the migration state",
		}, m.quarantine(t).Reason)
	})
}

func TestReferenceCannotReceive(t *testing.T) {
	m := newMirror(t, None[[]state.KindNode]())
	stream, err := m.reference.Receive(t.Context(), &eventpb.ReceiveRequest{})
	if err == nil {
		_, err = stream.Receive()
	}
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

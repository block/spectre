package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/comparison/javascript/javascripttest"
	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/schema"
)

// fake is the protocol of the test binding and other is one no script declares, so the
// engine pipelines are tested apart from any real protocol.
const (
	fake  = model.Protocol("fake")
	other = model.Protocol("other")
)

const schemaDeclarations = `
declare module "test.v1" {
  export interface Response { value: string; trace?: string }
  export interface Query { value: string; trace?: string }
}
`

// unknown is the type of a value no schema type describes.
var unknown schema.Type = schema.UnknownType{}

// textFields are the fields of the test.v1 object types schemaDeclarations declares.
var textFields = []schema.Field{
	{Name: "value", Type: schema.StringType{}},
	{Name: "trace", Optional: true, Type: schema.StringType{}},
}

// instance checks value as declared.
func instance(t *testing.T, declared schema.Type, value map[string]any) schema.Value {
	t.Helper()
	data, err := json.Marshal(value)
	assert.NoError(t, err)
	made, err := schema.Decode(declared, data)
	assert.NoError(t, err)
	return made
}

const fakeScript = `
import * as spectre from "spectre";
import type * as v1 from "test.v1";
spectre.egress.fake("GET fake.example/query").match<v1.Query>();
spectre.field<v1.Response, "trace">(() => undefined);
spectre.field<v1.Query, "trace">(() => undefined);
`

func TestNewEngineRejectsNoQuarantiner(t *testing.T) {
	recording := &recordingQuarantiner{}
	for name, test := range map[string]struct {
		ingress, egress engine.Quarantiner
	}{
		"NoIngress": {egress: recording},
		"NoEgress":  {ingress: recording},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := engine.NewEngine(t.Context(), newConfig(t, fakeScript, time.Second), []javascript.Binding{javascripttest.Binding()}, test.ingress, test.egress, None[engine.CallCounter]())
			assert.EqualError(t, err, "quarantiner is required")
		})
	}
}

func TestNewEngineRejectsInvalidMatchWindow(t *testing.T) {
	recording := &recordingQuarantiner{}
	_, err := engine.NewEngine(t.Context(), newConfig(t, fakeScript, 0), []javascript.Binding{javascripttest.Binding()}, recording, recording, None[engine.CallCounter]())
	assert.EqualError(t, err, "match window must be positive")
}

func TestEngineQuarantinesThroughTheQuarantinerOfItsDirection(t *testing.T) {
	ingress, egress := &recordingQuarantiner{}, &recordingQuarantiner{}
	pairing, err := engine.NewEngine(t.Context(), newConfig(t, fakeScript, time.Second), []javascript.Binding{javascripttest.Binding()}, ingress, egress, None[engine.CallCounter]())
	assert.NoError(t, err)

	_, err = pairing.IngressCandidate(fake).ProcessRequest(t.Context(), None[engine.RequestID](), "compared", notIgnored, unreadPayload(t))
	assert.Error(t, err)
	assert.Equal(t, []string{"candidate request has no request ID"}, ingress.Reasons())
	assert.Equal(t, nil, egress.Reasons())

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = pairing.EgressCandidate(fake).ProcessRequest(ctx, Some(newRequestID()), "query", notIgnored, query(t, queryType, "london", ""))
	assert.Error(t, err)
	assert.Equal(t, 1, len(egress.Reasons()))
	assert.Equal(t, 1, len(ingress.Reasons()))
}

// harness is an engine for the fake protocol with recording collaborators.
type harness struct {
	// ctx carries the logger that writes to logs.
	ctx        context.Context
	engine     *engine.Engine
	quarantine *recordingQuarantiner
	calls      *proxy.CallCounter
	logs       *lockedBuffer
	// requestID is the mirrored request that egress requests belong to, unless a test says otherwise.
	requestID engine.RequestID
}

func newHarness(t *testing.T, window time.Duration) *harness {
	t.Helper()
	return newScriptHarness(t, fakeScript, window)
}

func newScriptHarness(t *testing.T, script string, window time.Duration) *harness {
	t.Helper()
	logs := &lockedBuffer{}
	quarantine := &recordingQuarantiner{}
	calls := proxy.NewCallCounter()
	ctx := logger.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	pairing, err := engine.NewEngine(ctx, newConfig(t, script, window), []javascript.Binding{javascripttest.Binding()}, quarantine, quarantine, Some[engine.CallCounter](calls))
	assert.NoError(t, err)
	return &harness{
		ctx: ctx, engine: pairing, quarantine: quarantine, calls: calls, logs: logs,
		requestID: newRequestID(),
	}
}

// records returns every log record with msg, without its timestamp.
func (h *harness) records(t *testing.T, msg string) []map[string]any {
	t.Helper()
	records := []map[string]any{}
	for line := range bytes.Lines(h.logs.Bytes()) {
		record := map[string]any{}
		assert.NoError(t, json.Unmarshal(line, &record))
		if record["msg"] != msg {
			continue
		}
		delete(record, "time")
		records = append(records, record)
	}
	return records
}

func newConfig(t *testing.T, script string, window time.Duration) engine.Config {
	t.Helper()
	scripts, schema := t.TempDir(), t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(scripts, "fake.ts"), []byte(script), 0o600))
	assert.NoError(t, os.WriteFile(filepath.Join(schema, "test.d.ts"), []byte(schemaDeclarations), 0o600))
	config := engine.NewConfig()
	config.Comparison.ScriptsDir = scripts
	config.Comparison.Schema.SchemaDir = schema
	config.MatchWindow = window
	return config
}

type recordingQuarantiner struct {
	mu      sync.Mutex
	reasons []model.QuarantineReason
}

func (q *recordingQuarantiner) Quarantine(_ context.Context, reason model.QuarantineReason) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.reasons = append(q.reasons, reason)
}

// Reasons describes each recorded reason as the logs do.
func (q *recordingQuarantiner) Reasons() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var described []string
	for _, reason := range q.reasons {
		described = append(described, reason.String())
	}
	return described
}

func (q *recordingQuarantiner) Recorded() []model.QuarantineReason {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.reasons)
}

// countingRaw is a recorded response that counts how often it is released.
type countingRaw struct {
	name     string
	released atomic.Int32
}

func newRaw(name string) *countingRaw {
	return &countingRaw{name: name}
}

func (r *countingRaw) Release() {
	r.released.Add(1)
}

func (r *countingRaw) Releases() int {
	return int(r.released.Load())
}

// awaitRelease waits for raw to be released once, failing after a second.
func awaitRelease(t *testing.T, raw *countingRaw) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for raw.Releases() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	assert.Equal(t, 1, raw.Releases())
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

func newRequestID() engine.RequestID {
	return typeid.Must(typeid.New[engine.RequestID]())
}

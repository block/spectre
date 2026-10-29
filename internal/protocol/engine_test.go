package protocol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocol"
)

// fake and other are protocols no script declares, so the engine pipelines are
// tested apart from HTTP.
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

const fakeScript = `
import * as spectre from "spectre";
import type * as v1 from "test.v1";
spectre.ingress.http("GET /compared").match<v1.Response>();
spectre.ingress.http("GET /ignored").ignore();
spectre.ingress.http("GET /normalised").match<v1.Response>(({ status, body }) => ({ status: status === 201 ? 200 : status, body }));
spectre.ingress.http("GET /bodyonly").match<v1.Response>(({ body }) => ({ body }));
spectre.egress.http("GET fake.example/query").match<v1.Query>();
spectre.field<v1.Response, "trace">(() => undefined);
spectre.field<v1.Query, "trace">(() => undefined);
`

func TestNewEngineDeclaresEndpointsToTheirMatchers(t *testing.T) {
	fakeMatcher, httpMatcher := &recordingMatcher{}, &recordingMatcher{}
	config := newConfig(t, fakeScript, time.Second)

	//nolint:exhaustive // the engine is tested with fake protocols, not real ones
	_, err := protocol.NewEngine(t.Context(), config,
		map[model.Protocol]protocol.Matcher{fake: fakeMatcher, model.ProtocolHTTP: httpMatcher}, &recordingQuarantiner{})

	assert.NoError(t, err)
	assert.Equal(t, []string{
		`egress endpoint "GET fake.example/query"`,
		`ingress rule http("GET /compared")`,
		`ingress rule http("GET /ignored")`,
		`ingress rule http("GET /normalised")`,
		`ingress rule http("GET /bodyonly")`,
	}, httpMatcher.declared)
	assert.Equal(t, []string(nil), fakeMatcher.declared)
}

func TestNewEngineRejectsInvalidSetup(t *testing.T) {
	for name, test := range map[string]struct {
		matchers   map[model.Protocol]protocol.Matcher
		quarantine protocol.Quarantiner
		expected   string
	}{
		"UnregisteredProtocol": {
			//nolint:exhaustive // the engine is tested with fake protocols, not real ones
			matchers:   map[model.Protocol]protocol.Matcher{fake: &recordingMatcher{}},
			quarantine: &recordingQuarantiner{},
			expected:   `egress endpoint "GET fake.example/query" names unregistered protocol "http"`,
		},
		"MatcherRejectsPattern": {
			matchers:   map[model.Protocol]protocol.Matcher{model.ProtocolHTTP: &recordingMatcher{err: Some(errors.New("bad pattern"))}},
			quarantine: &recordingQuarantiner{},
			expected:   `declare egress endpoint "GET fake.example/query": bad pattern`,
		},
		"NoQuarantiner": {
			matchers: map[model.Protocol]protocol.Matcher{model.ProtocolHTTP: &recordingMatcher{}},
			expected: "quarantiner is required",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := protocol.NewEngine(t.Context(), newConfig(t, fakeScript, time.Second), test.matchers, test.quarantine)
			assert.EqualError(t, err, test.expected)
		})
	}
}

// harness is an engine for the fake protocol with recording collaborators.
type harness struct {
	// ctx carries the logger that writes to logs.
	ctx        context.Context
	engine     *protocol.Engine
	quarantine *recordingQuarantiner
	logs       *lockedBuffer
	// rules are the fake script's ingress rules in declaration order.
	rules []javascript.Rule
}

func newHarness(t *testing.T, window time.Duration) *harness {
	t.Helper()
	logs := &lockedBuffer{}
	quarantine := &recordingQuarantiner{}
	rules := &recordingMatcher{}
	ctx := logger.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	engine, err := protocol.NewEngine(ctx, newConfig(t, fakeScript, window),
		//nolint:exhaustive // the engine is tested with fake protocols, not real ones
		map[model.Protocol]protocol.Matcher{fake: &recordingMatcher{}, model.ProtocolHTTP: rules}, quarantine)
	assert.NoError(t, err)
	return &harness{ctx: ctx, engine: engine, quarantine: quarantine, logs: logs, rules: rules.rules}
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

func newConfig(t *testing.T, script string, window time.Duration) protocol.Config {
	t.Helper()
	scripts, schema := t.TempDir(), t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(scripts, "fake.ts"), []byte(script), 0o600))
	assert.NoError(t, os.WriteFile(filepath.Join(schema, "test.d.ts"), []byte(schemaDeclarations), 0o600))
	config := protocol.NewConfig()
	config.Comparison.ScriptsDir = scripts
	config.Comparison.Schema.SchemaDir = schema
	config.MatchWindow = window
	return config
}

type recordingMatcher struct {
	declared []string
	rules    []javascript.Rule
	err      Option[error]
}

func (m *recordingMatcher) Declare(declaration protocol.Declaration) error {
	switch declared := declaration.(type) {
	case protocol.DeclarationRequest:
		m.declared = append(m.declared, fmt.Sprintf("egress endpoint %q", declared.Pattern))
	case protocol.DeclarationRule:
		m.declared = append(m.declared, "ingress rule "+declared.Rule.String())
		m.rules = append(m.rules, declared.Rule)
	}
	if err, failed := m.err.Get(); failed {
		return err
	}
	return nil
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

// awaitReleases waits for raw to be released expected times, failing after a second.
func awaitReleases(t *testing.T, raw *countingRaw, expected int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for raw.Releases() != expected && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	assert.Equal(t, expected, raw.Releases())
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

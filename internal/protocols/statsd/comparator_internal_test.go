package statsd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/statsd/metric"
	"github.com/block/spectre/internal/protocols/statsd/scripted"
	"github.com/block/spectre/internal/proxy"
)

// feeplansScript maps the Java candidate's framework metrics onto the Ruby reference's.
const feeplansScript = `
	egress.statsd("jvm.**").ignore();
	egress.statsd("framework.actions.service_api.response_codes").tag("code", (code) => code !== undefined && code.endsWith("s")).ignore();
	egress.statsd("framework.actions.service_api.response_codes").map((m) => ({ name: "http.responses", tags: { endpoint: m.tags.api, code: m.tags.code } }));
	egress.statsd("framework.http.response_codes").map((m) => m.side === "reference"
		? { name: "http.responses", tags: { endpoint: m.tags.controller + "#" + m.tags.action, code: m.tags.code } }
		: undefined);
	egress.statsd("framework.actions.*.*.response_times").map((m) => ({ name: "http.latency", tags: { endpoint: m.name.split(".")[3] } }));
	egress.statsd("http.responses").compare({ relative: 0.1, absolute: 1 });
	egress.statsd("http.latency").compare({ relative: 0.1, p95: { relative: 0.2, only: "higher" } });
	egress.statsd("queue.depth").compare({});
`

func TestResolverMapsComparesAndIgnores(t *testing.T) {
	resolve := newTestResolver(t)
	for name, test := range map[string]struct {
		side     metric.Side
		series   metric.Series
		outcome  outcome
		resolved string
	}{
		"Ignored":         {side: metric.Candidate, series: series("jvm.gc.pause", metric.Timer, map[string]string{}), outcome: ignored, resolved: "jvm.gc.pause"},
		"IgnoredByTag":    {side: metric.Candidate, series: series("framework.actions.service_api.response_codes", metric.Counter, map[string]string{"code": "200s"}), outcome: ignored, resolved: "framework.actions.service_api.response_codes{code:200s}"},
		"MappedCandidate": {side: metric.Candidate, series: series("framework.actions.service_api.response_codes", metric.Counter, map[string]string{"api": "fees#show", "code": "200"}), outcome: compared, resolved: "http.responses{code:200,endpoint:fees#show}"},
		"MappedReference": {side: metric.Reference, series: series("framework.http.response_codes", metric.Counter, map[string]string{"controller": "fees", "action": "show", "code": "200"}), outcome: compared, resolved: "http.responses{code:200,endpoint:fees#show}"},
		"IgnoredBySide":   {side: metric.Candidate, series: series("framework.http.response_codes", metric.Counter, map[string]string{}), outcome: ignored, resolved: "framework.http.response_codes"},
		"MappedByName":    {side: metric.Candidate, series: series("framework.actions.Fees.show.response_times", metric.Distribution, map[string]string{}), outcome: compared, resolved: "http.latency{endpoint:show}"},
		"Uncovered":       {side: metric.Reference, series: series("ruby.gc.count", metric.Gauge, map[string]string{}), outcome: uncovered, resolved: "ruby.gc.count"},
	} {
		t.Run(name, func(t *testing.T) {
			resolutions, fresh, err := resolve.resolve(t.Context(), test.side, []metric.Series{test.series})
			assert.NoError(t, err)
			assert.Equal(t, []int{0}, fresh)
			assert.Equal(t, test.outcome, resolutions[0].outcome)
			assert.Equal(t, test.resolved, resolutions[0].series.String())

			_, fresh, err = resolve.resolve(t.Context(), test.side, []metric.Series{test.series})
			assert.NoError(t, err)
			assert.Equal(t, 0, len(fresh))
		})
	}
}

func TestWindowVerdicts(t *testing.T) {
	responses := resolution{outcome: compared, series: series("http.responses", metric.Counter, map[string]string{}), scale: 1,
		comparison: scripted.Comparison{Tolerance: scripted.Tolerance{Relative: 0.1, Absolute: 1}}}
	latency := resolution{outcome: compared, series: series("http.latency", metric.Timer, map[string]string{}), scale: 1,
		comparison: scripted.Comparison{Percentiles: []scripted.Percentile{{Rank: 95, Tolerance: scripted.Tolerance{Relative: 0.2, Only: scripted.Higher}}}, Tolerance: scripted.Tolerance{Relative: 0.5}}}
	depth := resolution{outcome: compared, series: series("queue.depth", metric.Gauge, map[string]string{}), scale: 1}
	for name, test := range map[string]struct {
		resolved  resolution
		reference []string
		candidate []string
		kind      model.QuarantineKind
		detail    string
	}{
		"WithinTolerance":     {resolved: responses, reference: []string{"r:10|c"}, candidate: []string{"r:9|c"}},
		"SampleRateCounted":   {resolved: responses, reference: []string{"r:10|c"}, candidate: []string{"r:5|c|@0.5"}},
		"CandidateLeftOut":    {resolved: responses, reference: []string{"r:10|c"}, kind: model.QuarantineUnmatched, detail: "candidate left out counter http.responses, which the reference sent with total 10"},
		"RareSeriesTolerated": {resolved: responses, reference: []string{"r:1|c"}},
		"CandidateExtra":      {resolved: responses, candidate: []string{"r:3|c"}, kind: model.QuarantineUnmatched, detail: "candidate sent counter http.responses with total 3, which the reference did not send"},
		"Divergent":           {resolved: responses, reference: []string{"r:20|c"}, candidate: []string{"r:10|c"}, kind: model.QuarantineDivergent, detail: "counter http.responses total is 10 on the candidate and 20 on the reference"},
		"FasterCandidate":     {resolved: latency, reference: []string{"r:100:100|ms"}, candidate: []string{"r:10:10|ms"}},
		"SlowerCandidate":     {resolved: latency, reference: []string{"r:100:100|ms"}, candidate: []string{"r:200:200|ms"}, kind: model.QuarantineDivergent, detail: "distribution http.latency p95 is "},
		"GaugeMissing":        {resolved: depth, reference: []string{"r:3|g"}, kind: model.QuarantineUnmatched, detail: "candidate left out gauge queue.depth"},
		"Uncovered":           {resolved: resolution{outcome: uncovered, series: series("ruby.gc", metric.Counter, map[string]string{}), scale: 1}, reference: []string{"r:1|c"}, kind: model.QuarantineUnable, detail: "no StatsD rule compares or ignores counter ruby.gc from the reference"},
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
			counted := newWindow(start)
			for side, datagrams := range map[metric.Side][]string{metric.Reference: test.reference, metric.Candidate: test.candidate} {
				for _, datagram := range datagrams {
					samples, _ := metric.Parse([]byte(datagram))
					counted.add(side, samples[0], test.resolved)
				}
			}
			reason, failed := counted.verdict(time.Minute).Get()
			if test.kind == "" {
				assert.False(t, failed, reason.Detail)
				return
			}
			assert.True(t, failed)
			assert.Equal(t, test.kind, reason.Kind)
			assert.Contains(t, reason.Detail, test.detail)
			assert.Contains(t, reason.Detail, "in the 1m0s window from 12:00:00")
			assert.Equal(t, Some(model.Traffic{Direction: model.Egress, Protocol: scripted.Protocol, Route: test.resolved.series.Route()}), reason.Traffic)
		})
	}
}

// Windows before the candidate's first compared metric are dropped, so a candidate that is still
// starting is not quarantined, and later windows are judged once they settle.
func TestComparatorJudgesSettledWindowsAfterTheCandidateStarts(t *testing.T) {
	quarantines := &recordingQuarantiner{}
	config := NewConfig()
	config.Window, config.Settle = time.Minute, 10*time.Second
	compare := newComparator(t.Context(), config, newTestResolver(t), quarantines, proxy.NewCandidates(proxy.NewCandidatesConfig()))
	origin := compare.origin
	at := func(offset time.Duration) time.Time { return origin.Add(offset) }
	reference := "framework.http.response_codes:10|c|#controller:fees,action:show,code:200"
	candidate := "framework.actions.service_api.response_codes:10|c|#api:fees#show,code:200"

	compare.add(t.Context(), observation{side: metric.Reference, at: at(time.Second), datagram: []byte(reference)})
	compare.add(t.Context(), observation{side: metric.Candidate, at: at(50 * time.Second), datagram: []byte(candidate)})
	compare.add(t.Context(), observation{side: metric.Reference, at: at(61 * time.Second), datagram: []byte(reference)})
	compare.add(t.Context(), observation{side: metric.Candidate, at: at(62 * time.Second), datagram: []byte(candidate)})
	compare.add(t.Context(), observation{side: metric.Reference, at: at(121 * time.Second), datagram: []byte(reference)})

	compare.judge(t.Context(), at(130*time.Second))
	assert.Equal(t, 0, len(quarantines.reasons))
	compare.judge(t.Context(), at(190*time.Second))
	assert.Equal(t, 1, len(quarantines.reasons))
	assert.Equal(t, model.QuarantineUnmatched, quarantines.reasons[0].Kind)
	assert.Contains(t, quarantines.reasons[0].Detail, "in the 1m0s window from "+at(2*time.Minute).UTC().Format(time.TimeOnly))
}

// A candidate that never sends a compared metric is judged once its startup timeout passes.
func TestComparatorJudgesASilentCandidateAfterItsStartupTimeout(t *testing.T) {
	quarantines := &recordingQuarantiner{}
	config := NewConfig()
	config.Window, config.Settle, config.CandidateStartupTimeout = time.Minute, 0, 2*time.Minute
	compare := newComparator(t.Context(), config, newTestResolver(t), quarantines, proxy.NewCandidates(proxy.NewCandidatesConfig()))
	reference := []byte("framework.http.response_codes:10|c|#controller:fees,action:show,code:200")
	for minute := range 3 {
		compare.add(t.Context(), observation{side: metric.Reference, at: compare.origin.Add(time.Duration(minute)*time.Minute + time.Second), datagram: reference})
	}

	compare.judge(t.Context(), compare.origin.Add(2*time.Minute))
	assert.Equal(t, 0, len(quarantines.reasons))
	compare.judge(t.Context(), compare.origin.Add(3*time.Minute))
	assert.Equal(t, 1, len(quarantines.reasons))
	assert.Contains(t, quarantines.reasons[0].Detail, "candidate left out counter http.responses{code:200,endpoint:fees#show}")
}

// Once either direction quarantines the candidate it gets no traffic, so its missing metrics never quarantine it again.
func TestComparatorStopsOnceTheCandidateIsQuarantined(t *testing.T) {
	quarantines := &recordingQuarantiner{}
	config := NewConfig()
	config.Window, config.Settle, config.CandidateStartupTimeout = time.Minute, 0, 0
	ingress := proxy.NewCandidates(proxy.NewCandidatesConfig())
	compare := newComparator(t.Context(), config, newTestResolver(t), quarantines, ingress)
	reference := []byte("framework.http.response_codes:10|c|#controller:fees,action:show,code:200")
	compare.add(t.Context(), observation{side: metric.Reference, at: compare.origin.Add(time.Second), datagram: reference})
	ingress.Quarantine(t.Context(), model.QuarantineReason{Kind: model.QuarantineDivergent, Detail: "ingress"})

	compare.judge(t.Context(), compare.origin.Add(2*time.Minute))

	assert.Equal(t, 0, len(quarantines.reasons))
}

// recordingQuarantiner records quarantine reasons.
type recordingQuarantiner struct {
	reasons []model.QuarantineReason
}

func (q *recordingQuarantiner) Quarantine(_ context.Context, reason model.QuarantineReason) {
	q.reasons = append(q.reasons, reason)
}

func series(name string, metricType metric.Type, tags map[string]string) metric.Series {
	return metric.Series{Name: name, Type: metricType, Tags: tags}
}

// newTestResolver loads feeplansScript with egress imported, and resolves by its StatsD rules.
func newTestResolver(t *testing.T) *resolver {
	t.Helper()
	config := comparison.NewConfig()
	config.ScriptsDir, config.Schema.SchemaDir = t.TempDir(), t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(config.ScriptsDir, "statsd.ts"), []byte(`import { egress } from "spectre";`+feeplansScript), 0o600))
	binding, handle := scripted.NewBinding()
	loaded, err := comparison.New(t.Context(), config, []javascript.Binding{binding})
	assert.NoError(t, err)
	resolved, err := newResolver(loaded, handle)
	assert.NoError(t, err)
	return resolved
}

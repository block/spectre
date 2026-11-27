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
	egress.statsd("http.responses").compare({ total: { upper: 0.1, lower: 0.1 } });
	egress.statsd("http.latency").compare({ count: { upper: 0.1, lower: 0.1 }, p95: { upper: 0.2, lower: 1 } });
	egress.statsd("queue.depth").compare({ mean: {} });
`

func TestResolverMapsComparesAndIgnores(t *testing.T) {
	resolve := newTestResolver(t)
	for name, test := range map[string]struct {
		side     model.Side
		series   metric.Series
		outcome  outcome
		resolved string
	}{
		"Ignored":         {side: model.Candidate, series: series("jvm.gc.pause", metric.Timer, map[string]string{}), outcome: ignored, resolved: "jvm.gc.pause"},
		"IgnoredByTag":    {side: model.Candidate, series: series("framework.actions.service_api.response_codes", metric.Counter, map[string]string{"code": "200s"}), outcome: ignored, resolved: "framework.actions.service_api.response_codes{code:200s}"},
		"MappedCandidate": {side: model.Candidate, series: series("framework.actions.service_api.response_codes", metric.Counter, map[string]string{"api": "fees#show", "code": "200"}), outcome: compared, resolved: "http.responses{code:200,endpoint:fees#show}"},
		"MappedReference": {side: model.Reference, series: series("framework.http.response_codes", metric.Counter, map[string]string{"controller": "fees", "action": "show", "code": "200"}), outcome: compared, resolved: "http.responses{code:200,endpoint:fees#show}"},
		"IgnoredBySide":   {side: model.Candidate, series: series("framework.http.response_codes", metric.Counter, map[string]string{}), outcome: ignored, resolved: "framework.http.response_codes"},
		"MappedByName":    {side: model.Candidate, series: series("framework.actions.Fees.show.response_times", metric.Distribution, map[string]string{}), outcome: compared, resolved: "http.latency{endpoint:show}"},
		"Uncovered":       {side: model.Reference, series: series("ruby.gc.count", metric.Gauge, map[string]string{}), outcome: uncovered, resolved: "ruby.gc.count"},
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

func TestResolverRejectsATagFunctionThatReturnsANonBoolean(t *testing.T) {
	resolve := newScriptResolver(t, `egress.statsd("http.**").tag("code", ((code: string | undefined) => code) as never).ignore();`)
	_, _, err := resolve.resolve(t.Context(), model.Reference, []metric.Series{series("http.responses", metric.Counter, map[string]string{"code": "200"})})
	assert.EqualError(t, err, "StatsD rules for http.responses{code:200}: StatsD tag function returned a value that is not a boolean")
}

func TestWindowVerdicts(t *testing.T) {
	responses := resolution{outcome: compared, series: series("http.responses", metric.Counter, map[string]string{"code": "200", "endpoint": "fees#show"}), scale: 1,
		comparison: scripted.Comparison{Checks: []scripted.Check{{Statistic: metric.Total, Tolerance: scripted.Tolerance{Upper: 0.1, Lower: 0.1}}}}}
	latencyComparison := scripted.Comparison{Checks: []scripted.Check{
		{Statistic: metric.Count, Tolerance: scripted.Tolerance{Upper: 0.5, Lower: 0.5}},
		{Statistic: metric.P95, Tolerance: scripted.Tolerance{Upper: 0.2, Lower: 1}},
	}}
	latency := resolution{outcome: compared, series: series("http.latency", metric.Timer, map[string]string{}), scale: 1, comparison: latencyComparison}
	latencyAsCounter := resolution{outcome: compared, series: series("http.latency", metric.Counter, map[string]string{}), scale: 1, comparison: latencyComparison}
	depth := resolution{outcome: compared, series: series("queue.depth", metric.Gauge, map[string]string{}), scale: 1,
		comparison: scripted.Comparison{Checks: []scripted.Check{{Statistic: metric.Mean, Tolerance: scripted.Tolerance{}}}}}
	optional := resolution{outcome: compared, series: series("http.retries", metric.Counter, map[string]string{}), scale: 1,
		comparison: scripted.Comparison{Checks: []scripted.Check{{Statistic: metric.Total, Tolerance: scripted.Tolerance{Upper: 0.1, Lower: 1}}}}}
	changed := func(statistics ...metric.Statistic) []model.Difference {
		differences := []model.Difference{}
		for _, statistic := range statistics {
			differences = append(differences, model.Difference{Path: string(statistic), Change: model.ChangeChanged, Count: 1})
		}
		return differences
	}
	for name, test := range map[string]struct {
		resolved    resolution
		reference   []string
		candidate   []string
		kind        model.QuarantineKind
		differences []model.Difference
		detail      string
	}{
		"WithinTolerance":    {resolved: responses, reference: []string{"r:10|c"}, candidate: []string{"r:9|c"}},
		"SampleRateCounted":  {resolved: responses, reference: []string{"r:10|c"}, candidate: []string{"r:5|c|@0.5"}},
		"CandidateLeftOut":   {resolved: responses, reference: []string{"r:10|c"}, kind: model.QuarantineUnmatched, detail: "candidate left out counter http.responses{code,endpoint} that the reference sent: total below the 10% lower bound"},
		"LeftOutWithinLower": {resolved: optional, reference: []string{"r:3|c"}},
		"CandidateExtra":     {resolved: responses, candidate: []string{"r:3|c"}, kind: model.QuarantineUnmatched, detail: "candidate sent counter http.responses{code,endpoint} that the reference did not"},
		"Divergent":          {resolved: responses, reference: []string{"r:20|c"}, candidate: []string{"r:10|c"}, kind: model.QuarantineDivergent, differences: changed(metric.Total), detail: "counter http.responses{code,endpoint}: total below the 10% lower bound"},
		"FasterCandidate":    {resolved: latency, reference: []string{"r:100:100|ms"}, candidate: []string{"r:10:10|ms"}},
		"SlowerCandidate":    {resolved: latency, reference: []string{"r:100:100|ms"}, candidate: []string{"r:200:200|ms"}, kind: model.QuarantineDivergent, differences: changed(metric.P95), detail: "distribution http.latency: p95 above the 20% upper bound"},
		"FewerSamples":       {resolved: latency, reference: []string{"r:1:1:1:1|ms"}, candidate: []string{"r:1|ms"}, kind: model.QuarantineDivergent, differences: changed(metric.Count), detail: "distribution http.latency: count below the 50% lower bound"},
		"FewerAndSlower":     {resolved: latency, reference: []string{"r:1:1:1:1|ms"}, candidate: []string{"r:5|ms"}, kind: model.QuarantineDivergent, differences: changed(metric.Count, metric.P95), detail: "distribution http.latency: count below the 50% lower bound, p95 above the 20% upper bound"},
		"StatisticNotOfType": {resolved: latencyAsCounter, reference: []string{"r:1|c"}, candidate: []string{"r:1|c"}, kind: model.QuarantineUnable, detail: "the StatsD rule compares count and p95, which counter http.latency does not have"},
		"GaugeMissing":       {resolved: depth, reference: []string{"r:3|g"}, kind: model.QuarantineUnmatched, detail: "candidate left out gauge queue.depth that the reference sent"},
		"GaugeDiffers":       {resolved: depth, reference: []string{"r:3|g"}, candidate: []string{"r:4|g"}, kind: model.QuarantineDivergent, differences: changed(metric.Mean), detail: "gauge queue.depth: mean above the 0% upper bound"},
		"Uncovered":          {resolved: resolution{outcome: uncovered, series: series("ruby.gc", metric.Counter, map[string]string{"pid": "42"}), scale: 1}, reference: []string{"r:1|c"}, kind: model.QuarantineUnable, detail: "no StatsD rule compares or ignores counter ruby.gc{pid} from the reference"},
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
			counted := newWindow(start)
			for side, datagrams := range map[model.Side][]string{model.Reference: test.reference, model.Candidate: test.candidate} {
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
			assert.Equal(t, model.QuarantineReason{
				Kind:        test.kind,
				Traffic:     Some(model.Traffic{Direction: model.Egress, Protocol: scripted.Protocol, Route: test.resolved.series.Route()}),
				Differences: test.differences,
				Detail:      test.detail + ", in the 1m0s window from 12:00:00",
			}, reason)
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

	compare.add(t.Context(), observation{side: model.Reference, at: at(time.Second), datagram: []byte(reference)})
	compare.add(t.Context(), observation{side: model.Candidate, at: at(50 * time.Second), datagram: []byte(candidate)})
	compare.add(t.Context(), observation{side: model.Reference, at: at(61 * time.Second), datagram: []byte(reference)})
	compare.add(t.Context(), observation{side: model.Candidate, at: at(62 * time.Second), datagram: []byte(candidate)})
	compare.add(t.Context(), observation{side: model.Reference, at: at(121 * time.Second), datagram: []byte(reference)})

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
		compare.add(t.Context(), observation{side: model.Reference, at: compare.origin.Add(time.Duration(minute)*time.Minute + time.Second), datagram: reference})
	}

	compare.judge(t.Context(), compare.origin.Add(2*time.Minute))
	assert.Equal(t, 0, len(quarantines.reasons))
	compare.judge(t.Context(), compare.origin.Add(3*time.Minute))
	assert.Equal(t, 1, len(quarantines.reasons))
	assert.Contains(t, quarantines.reasons[0].Detail, "candidate left out counter http.responses{code,endpoint} that the reference sent")
}

// Once either direction quarantines the candidate it gets no traffic, so its missing metrics never quarantine it again.
func TestComparatorStopsOnceTheCandidateIsQuarantined(t *testing.T) {
	quarantines := &recordingQuarantiner{}
	config := NewConfig()
	config.Window, config.Settle, config.CandidateStartupTimeout = time.Minute, 0, 0
	ingress := proxy.NewCandidates(proxy.NewCandidatesConfig())
	compare := newComparator(t.Context(), config, newTestResolver(t), quarantines, ingress)
	reference := []byte("framework.http.response_codes:10|c|#controller:fees,action:show,code:200")
	compare.add(t.Context(), observation{side: model.Reference, at: compare.origin.Add(time.Second), datagram: reference})
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
	return newScriptResolver(t, feeplansScript)
}

// newScriptResolver resolves with the rules of one script.
func newScriptResolver(t *testing.T, script string) *resolver {
	t.Helper()
	config := comparison.NewConfig()
	config.ScriptsDir, config.Schema.SchemaDir = t.TempDir(), t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(config.ScriptsDir, "statsd.ts"), []byte(`import { egress } from "spectre";`+script), 0o600))
	binding, handle := scripted.NewBinding()
	loaded, err := comparison.New(t.Context(), config, []javascript.Binding{binding})
	assert.NoError(t, err)
	resolved, err := newResolver(loaded, handle)
	assert.NoError(t, err)
	return resolved
}

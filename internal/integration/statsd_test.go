//go:build integration

package integration_test

import (
	"testing"

	"github.com/block/spectre/internal/integration/it"
	"github.com/block/spectre/internal/model"
)

// statsdScript maps the framework's response counters to http.responses, which it
// compares by total, and ignores everything else.
const statsdScript = `
	import { egress } from "spectre";

	egress.statsd("framework.actions.**").map((m) => ({ name: "http.responses", tags: { code: m.tags.code } }));
	egress.statsd("http.responses").compare({ total: { upper: 0.1, lower: 0.1 } });
	egress.statsd("sentinel.requests").compare({ total: { upper: 0.1, lower: 0.1 } });
	egress.statsd("jvm.**").ignore();
	egress.statsd("**").ignore();
`

func statsdMirror(t *testing.T) *it.Mirror {
	t.Helper()
	return it.Start(t,
		it.Scripts(statsdScript),
		it.WithStatsD(),
		it.Reference(sampleService()),
		it.Candidate(sampleService()),
	)
}

// Windows are judged in series order, so a quarantine for sentinel.requests, which sorts
// after http.responses, shows that the window's http.responses matched.
func TestMatchingStatsDMetricsAreForwardedWithoutQuarantine(t *testing.T) {
	m := statsdMirror(t)

	m.StatsD().SendCandidate("framework.actions.search.responses:3|c|#code:200\njvm.threads:20|g")
	m.StatsD().SendReference("framework.actions.search.responses:3|c|#code:200")
	m.StatsD().SendReference("sentinel.requests:1|c")
	m.StatsD().ExpectForwarded("framework.actions.search.responses:3|c|#code:200")
	m.StatsD().ExpectForwarded("sentinel.requests:1|c")
	m.ExpectQuarantineDetailPrefix(it.UnmatchedStatsD("sentinel.requests"), "candidate left out counter sentinel.requests that the reference sent: running total below the 10% lower bound, in the 200ms window from ")
}

func TestCandidateLeavingOutStatsDMetricIsQuarantined(t *testing.T) {
	m := statsdMirror(t)

	m.StatsD().SendCandidate("jvm.threads:20|g")
	m.StatsD().SendReference("framework.actions.search.responses:5|c|#code:200")
	m.StatsD().ExpectForwarded("framework.actions.search.responses:5|c|#code:200")
	m.ExpectQuarantineDetailPrefix(it.UnmatchedStatsD("http.responses{code}"), "candidate left out counter http.responses{code} that the reference sent: running total below the 10% lower bound, in the 200ms window from ")
}

func TestDivergentStatsDTotalQuarantinesCandidate(t *testing.T) {
	m := statsdMirror(t)

	m.StatsD().SendReference("framework.actions.search.responses:10|c|#code:200")
	m.StatsD().SendCandidate("framework.actions.search.responses:20|c|#code:200")
	m.StatsD().ExpectForwarded("framework.actions.search.responses:10|c|#code:200")
	m.ExpectQuarantineDetailPrefix(it.DivergentStatsD("http.responses{code}", model.Difference{Path: "total", Change: model.ChangeChanged, Count: 1}), "counter http.responses{code}: running total above the 10% upper bound, in the 200ms window from ")
}

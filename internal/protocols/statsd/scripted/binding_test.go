package scripted_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/protocols/statsd/scripted"
)

func TestPatternExpressionMatchesSegments(t *testing.T) {
	for name, test := range map[string]struct {
		pattern string
		matches []string
		misses  []string
	}{
		"Exact":          {pattern: "framework.http.response_codes", matches: []string{"framework.http.response_codes"}, misses: []string{"framework.http.response_codesX", "frameworkXhttp.response_codes"}},
		"WithinSegment":  {pattern: "feeplans-fe.cache_*", matches: []string{"feeplans-fe.cache_hits", "feeplans-fe.cache_"}, misses: []string{"feeplans-fe.cache_hits.total"}},
		"OneSegment":     {pattern: "framework.actions.*.*.response_times", matches: []string{"framework.actions.Fees.get.response_times"}, misses: []string{"framework.actions.Fees.response_times"}},
		"AcrossSegments": {pattern: "jvm.**", matches: []string{"jvm.gc.pause", "jvm.threads"}, misses: []string{"jvm", "other.jvm.gc"}},
		"Everything":     {pattern: "**", matches: []string{"a", "a.b.c"}},
	} {
		t.Run(name, func(t *testing.T) {
			expression, err := scripted.PatternExpression(test.pattern)
			assert.NoError(t, err)
			for _, name := range test.matches {
				assert.True(t, expression.MatchString(name), name)
			}
			for _, name := range test.misses {
				assert.False(t, expression.MatchString(name), name)
			}
		})
	}
}

func TestDeclaresRulesInOrder(t *testing.T) {
	loaded, handle, err := load(t, `
		egress.statsd("jvm.**").ignore();
		egress.statsd("framework.actions.**").tag("code", "200").tag("api", (api) => api !== undefined).map((m) => ({ name: "http" }));
		egress.statsd("http").compare({ relative: 0.05, absolute: 2, p95: { relative: 0.5, only: "higher" } });
	`)
	assert.NoError(t, err)

	assert.Equal(t, scripted.Declarations{Rules: []scripted.Rule{
		{Selector: scripted.Selector{Pattern: "jvm.**"}, Map: None[int](), Compare: None[scripted.Comparison]()},
		{
			Selector: scripted.Selector{Pattern: "framework.actions.**", Tags: []scripted.TagSelector{
				{Name: "code", Value: Some("200")},
				{Name: "api", Value: None[string](), Callback: 0},
			}},
			Map:     Some(0),
			Compare: None[scripted.Comparison](),
		},
		{
			Selector: scripted.Selector{Pattern: "http"},
			Map:      None[int](),
			Compare: Some(scripted.Comparison{
				Tolerance:   scripted.Tolerance{Relative: 0.05, Absolute: 2, Only: scripted.Both},
				Percentiles: []scripted.Percentile{{Rank: 95, Tolerance: scripted.Tolerance{Relative: 0.5, Only: scripted.Higher}}},
			}),
		},
	}}, handle.Declared(loaded.Program()))
}

func TestRejectsInvalidRules(t *testing.T) {
	for name, test := range map[string]struct {
		body    string
		message string
	}{
		"EmptyPattern":      {body: `egress.statsd("").ignore();`, message: "spectre.egress.statsd pattern must be a non-empty string"},
		"NegativeTolerance": {body: `egress.statsd("a").compare({ relative: -1 });`, message: "relative must be a number of at least 0"},
		"UnknownBound":      {body: `egress.statsd("a").compare({ only: "sideways" as "higher" });`, message: `only must be "higher" or "lower"`},
		"NoComparison":      {body: `egress.statsd("a").compare(undefined as never);`, message: "compare requires a comparison"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := load(t, test.body)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

// load loads one script with egress imported.
func load(t *testing.T, script string) (*comparison.Scripts, scripted.Handle, error) {
	t.Helper()
	config := comparison.NewConfig()
	config.ScriptsDir, config.Schema.SchemaDir = t.TempDir(), t.TempDir()
	source := `import { egress } from "spectre";` + script
	assert.NoError(t, os.WriteFile(filepath.Join(config.ScriptsDir, "statsd.ts"), []byte(source), 0o600))
	binding, handle := scripted.NewBinding()
	loaded, err := comparison.New(t.Context(), config, []javascript.Binding{binding})
	return loaded, handle, err
}

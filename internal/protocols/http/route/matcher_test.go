package route_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/protocols/http/route"
	"github.com/block/spectre/internal/protocols/http/script"
)

func TestMatcherReadsDeclarationsFromScripts(t *testing.T) {
	matcher, err := newMatcher(t, `
		spectre.ingress.http("GET /ignored").ignore();
		spectre.ingress.http("GET /compared").match<test.Query>();
		spectre.egress.http("GET weather.example/items/{id}").match<test.Query>();
	`)
	assert.NoError(t, err)

	assert.Equal(t, []route.Request{{Pattern: "GET weather.example/items/{id}", Type: "test.Query"}}, matcher.Requests())
	request, wildcards, matched := matcher.Request("GET", "weather.example", "/items/one")
	assert.True(t, matched)
	assert.Equal(t, "GET weather.example/items/{id}", request.Pattern)
	assert.Equal(t, map[string]string{"id": "one"}, wildcards)
	rules, ignored := matcher.Responses("GET", "/ignored", nil)
	assert.Equal(t, 0, len(rules))
	assert.Equal(t, Some(`rule http("GET /ignored") ignores the request`), ignored)
	rules, ignored = matcher.Responses("GET", "/compared", nil)
	assert.Equal(t, 1, len(rules))
	assert.Equal(t, None[string](), ignored)
}

func TestMatcherRejectsInvalidPatterns(t *testing.T) {
	for name, test := range map[string]struct {
		script   string
		expected string
	}{
		"IngressPattern": {script: `spectre.ingress.http("GET /{id").match<test.Query>();`, expected: `declare ingress rule http("GET /{id")`},
		"EgressPattern":  {script: `spectre.egress.http("GET weather.example/{id").match<test.Query>();`, expected: `declare egress endpoint "GET weather.example/{id"`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newMatcher(t, test.script)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.expected)
		})
	}
}

func newMatcher(t *testing.T, body string) (*route.Matcher, error) {
	t.Helper()
	config := comparison.NewConfig()
	config.ScriptsDir, config.Schema.SchemaDir = t.TempDir(), t.TempDir()
	declarations := `declare module "test" { interface Query { id?: string; } }`
	assert.NoError(t, os.WriteFile(filepath.Join(config.Schema.SchemaDir, "test.d.ts"), []byte(declarations), 0o600))
	imports := `import * as spectre from "spectre"; import type * as test from "test";`
	assert.NoError(t, os.WriteFile(filepath.Join(config.ScriptsDir, "test.ts"), []byte(imports+body), 0o600))
	binding, handle := script.New()
	scripts, err := comparison.New(t.Context(), config, []javascript.Binding{binding})
	assert.NoError(t, err)
	return route.NewMatcher(scripts.Endpoints(), handle.Declared(scripts.Program()).Rules)
}

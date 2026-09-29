package javascript_test

import (
	"sync"
	"testing"
	"testing/fstest"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/comparison/javascript"
)

func TestProgramOwnsDeclarations(t *testing.T) {
	program, err := compile(t, fstest.MapFS{
		"weather.js": script(`
			spectre.endpoint("GET /v1/forecast", "test.v1.Weather.Get");
			spectre.endpoint("GET /v2/forecast", "test.v1.Weather.GetV2");
			spectre.field("test.v1.Response.value", () => true);
		`),
		"users/user.js": script(`spectre.message("test.v1.User", () => true);`),
	})
	assert.NoError(t, err)

	assert.Equal(t, []endpoint{{"GET /v1/forecast", "test.v1.Weather.Get"}, {"GET /v2/forecast", "test.v1.Weather.GetV2"}}, endpoints(program))
	assert.Equal(t, []string{"test.v1.Response.value"}, program.Fields())
	assert.Equal(t, []string{"test.v1.User"}, program.Messages())
}

func TestLoadsEmptyScriptsDirectory(t *testing.T) {
	program, err := compile(t, fstest.MapFS{"README.md": {Data: []byte("Not a script.")}})
	assert.NoError(t, err)

	assert.Equal(t, []endpoint{}, endpoints(program))
	assert.Equal(t, []string{}, program.Fields())
	assert.Equal(t, []string{}, program.Messages())
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	evaluator.Close()
}

func TestRejectsInvalidEndpointDeclarations(t *testing.T) {
	for name, test := range map[string]struct {
		body    string
		message string
	}{
		"NoArguments":      {body: `spectre.endpoint();`, message: "requires a pattern and an RPC method"},
		"OneArgument":      {body: `spectre.endpoint("GET /v1/forecast");`, message: "requires a pattern and an RPC method"},
		"ThreeArguments":   {body: `spectre.endpoint("GET /v1/forecast", "test.v1.Weather.Get", 1);`, message: "requires a pattern and an RPC method"},
		"EmptyPattern":     {body: `spectre.endpoint("", "test.v1.Weather.Get");`, message: "pattern must be a non-empty string"},
		"NonStringPattern": {body: `spectre.endpoint(1, "test.v1.Weather.Get");`, message: "pattern must be a non-empty string"},
		"EmptyMethod":      {body: `spectre.endpoint("GET /v1/forecast", "");`, message: "method must be a non-empty string"},
		"NonStringMethod":  {body: `spectre.endpoint("GET /v1/forecast", {});`, message: "method must be a non-empty string"},
		"Duplicate": {
			body:    `spectre.endpoint("GET /v1/forecast", "test.v1.Weather.Get"); spectre.endpoint("GET /v1/forecast", "test.v1.Weather.Get");`,
			message: `duplicate endpoint "GET /v1/forecast"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newProgram(t, test.body)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestEvaluatesEachScriptOnce(t *testing.T) {
	// Every file is loaded and users.js is also imported twice; a second evaluation
	// would register a duplicate target.
	program, err := compile(t, fstest.MapFS{
		"weather.js": script(`
			import "./lib/users.js";
			import "./lib/accounts/owners.js";
			spectre.field("test.v1.Response.value", () => true);
		`),
		"lib/users.js": script(`spectre.message("test.v1.User", (user) => user);`),
		"lib/accounts/owners.js": script(`
			import "../users.js";
			spectre.field("test.v1.Response.owner", (owner) => owner);
		`),
	})
	assert.NoError(t, err)

	assert.Equal(t, []string{"test.v1.Response.owner", "test.v1.Response.value"}, program.Fields())
	assert.Equal(t, []string{"test.v1.User"}, program.Messages())
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	normalised, present, err := evaluator.NormaliseMessage("test.v1.User", "alice", true)
	assert.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, any("alice"), normalised)
}

func TestRejectsDuplicateTargetsAcrossScripts(t *testing.T) {
	_, err := compile(t, fstest.MapFS{
		"weather.js":   script(`spectre.message("test.v1.User", (user) => user);`),
		"lib/users.js": script(`spectre.message("test.v1.User", (user) => user);`),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `duplicate normaliser target "test.v1.User"`)
}

func TestRejectsInvalidImports(t *testing.T) {
	library := &fstest.MapFile{Data: []byte(`export const value = 1;`)}
	for name, test := range map[string]struct {
		files   fstest.MapFS
		message string
	}{
		"Bare": {
			files:   fstest.MapFS{"weather.js": script(`import "lib/users.js";`), "lib/users.js": library},
			message: `comparison module "weather.js" must import modules by relative path, not "lib/users.js"`,
		},
		"Escapes": {
			files:   fstest.MapFS{"weather.js": script(`import "../users.js";`)},
			message: `comparison module "weather.js" imports "../users.js" outside the scripts directory`,
		},
		"NestedEscapes": {
			files:   fstest.MapFS{"lib/users.js": script(`import "../../users.js";`)},
			message: `comparison module "lib/users.js" imports "../../users.js" outside the scripts directory`,
		},
		"Missing": {
			files:   fstest.MapFS{"weather.js": script(`import "./lib/missing.js";`)},
			message: `read comparison module "lib/missing.js"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(t, test.files)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestEvaluatorPreservesMissingAndNullArguments(t *testing.T) {
	program, err := newProgram(t, `
		spectre.field("test.v1.Response.value", (value) =>
			value === undefined ? "missing" : value === null ? "null" : "other");
	`)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	tests := map[string]struct {
		value    any
		present  bool
		expected any
	}{
		"Missing": {expected: "missing"},
		"Null":    {present: true, expected: "null"},
		"Value":   {value: "value", present: true, expected: "other"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			normalised, present, err := evaluator.NormaliseField("test.v1.Response.value", test.value, test.present)

			assert.NoError(t, err)
			assert.True(t, present)
			assert.Equal(t, test.expected, normalised)
		})
	}
}

func TestNormaliserResults(t *testing.T) {
	type result struct {
		Normalised any
		Present    bool
		Failed     bool
	}
	tests := map[string]struct {
		normaliser string
		expected   result
	}{
		"Sorted":                   {normaliser: `(value) => value.sort()`, expected: result{Normalised: []any{"a", "b"}, Present: true}},
		"Constant":                 {normaliser: `() => true`, expected: result{Normalised: true, Present: true}},
		"Null":                     {normaliser: `() => null`, expected: result{Present: true}},
		"UndefinedRemoves":         {normaliser: `() => undefined`},
		"UndefinedPropertyOmitted": {normaliser: `() => ({kept: 1, dropped: undefined})`, expected: result{Normalised: map[string]any{"kept": float64(1)}, Present: true}},
		"UndefinedElement":         {normaliser: `() => [undefined]`, expected: result{Failed: true}},
		"Function":                 {normaliser: `() => () => true`, expected: result{Failed: true}},
		"NestedFunction":           {normaliser: `() => ({nested: () => true})`, expected: result{Failed: true}},
		"NaN":                      {normaliser: `() => NaN`, expected: result{Failed: true}},
		"BigInt":                   {normaliser: `() => 1n`, expected: result{Failed: true}},
		"Throws":                   {normaliser: `() => { throw new Error("failed"); }`, expected: result{Failed: true}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			program, err := newProgram(t, `
				spectre.field("test.v1.Response.value", `+test.normaliser+`);
			`)
			assert.NoError(t, err)
			evaluator, err := program.NewEvaluator(t.Context())
			assert.NoError(t, err)
			defer evaluator.Close()

			normalised, present, err := evaluator.NormaliseField("test.v1.Response.value", []any{"b", "a"}, true)

			assert.Equal(t, test.expected, result{Normalised: normalised, Present: present, Failed: err != nil})
		})
	}
}

func TestNormalisersCannotReplaceJSONHelpers(t *testing.T) {
	program, err := newProgram(t, `
		JSON.stringify = () => "\"replaced\"";
		JSON.parse = () => "replaced";
		spectre.field("test.v1.Response.value", (value) => value);
	`)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()

	normalised, present, err := evaluator.NormaliseField("test.v1.Response.value", "original", true)

	assert.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, any("original"), normalised)
}

func TestRegistrationsRequireTwoArguments(t *testing.T) {
	tests := map[string]string{
		"FieldMissing": `spectre.field();`,
		"MessageOne":   `spectre.message("test.v1.Response");`,
		"FieldThree":   `spectre.field("test.v1.Response.value", () => true, "extra");`,
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newProgram(t, call)

			assert.Error(t, err)
		})
	}
}

func TestEvaluatorProtectsArgumentsFromMutation(t *testing.T) {
	program, err := newProgram(t, `
		spectre.message("test.v1.Response", (value) => {
			value.value = "changed";
			return value;
		});
	`)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	value := map[string]any{"value": "original"}

	normalised, present, err := evaluator.NormaliseMessage("test.v1.Response", value, true)

	assert.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, any(map[string]any{"value": "changed"}), normalised)
	assert.Equal(t, map[string]any{"value": "original"}, value)
}

func TestEvaluatorsKeepConcurrentRuntimesIsolated(t *testing.T) {
	program, err := newProgram(t, `
		let calls = 0;
		spectre.field("test.v1.Response.value", (value) => value + ":" + (++calls));
	`)
	assert.NoError(t, err)

	const evaluatorCount = 8
	type result struct {
		normalised any
		err        error
	}
	results := make(chan result, evaluatorCount)
	var wait sync.WaitGroup
	for range evaluatorCount {
		wait.Go(func() {
			evaluator, err := program.NewEvaluator(t.Context())
			if err != nil {
				results <- result{err: err}
				return
			}
			defer evaluator.Close()
			normalised, _, err := evaluator.NormaliseField("test.v1.Response.value", "same", true)
			results <- result{normalised: normalised, err: err}
		})
	}
	wait.Wait()
	close(results)

	for result := range results {
		assert.NoError(t, result.err)
		assert.Equal(t, any("same:1"), result.normalised)
	}
}

type endpoint struct {
	pattern string
	method  string
}

func endpoints(program *javascript.Program) []endpoint {
	declared := []endpoint{}
	for _, value := range program.Endpoints() {
		declared = append(declared, endpoint{value.Pattern(), value.Method()})
	}
	return declared
}

// script is a module with the spectre module imported.
func script(body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(`import * as spectre from "spectre";` + body)}
}

func newProgram(t *testing.T, body string) (*javascript.Program, error) {
	t.Helper()
	return compile(t, fstest.MapFS{"test.js": script(body)})
}

func compile(t *testing.T, files fstest.MapFS) (*javascript.Program, error) {
	t.Helper()
	return javascript.NewProgram(t.Context(), files)
}

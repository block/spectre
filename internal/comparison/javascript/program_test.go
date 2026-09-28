package javascript_test

import (
	"sync"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/comparison/javascript"
)

func TestProgramOwnsDeclaredTargets(t *testing.T) {
	program, err := javascript.NewProgram(t.Context(), "comparison.js", `
		import * as spectre from "spectre";
		spectre.rpc("test.v1.Service.Get", () => true);
		spectre.field("test.v1.Response.value", () => true);
		spectre.message("test.v1.Response", () => true);
	`)
	assert.NoError(t, err)

	assert.Equal(t, []string{"test.v1.Response.value"}, program.Fields())
	assert.Equal(t, []string{"test.v1.Response"}, program.Messages())
	assert.Equal(t, []string{"test.v1.Service.Get"}, program.RPCs())
}

func TestEvaluatorPreservesMissingAndNullArguments(t *testing.T) {
	program, err := javascript.NewProgram(t.Context(), "comparison.js", `
		import * as spectre from "spectre";
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
			program, err := javascript.NewProgram(t.Context(), "comparison.js", `
				import * as spectre from "spectre";
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
	program, err := javascript.NewProgram(t.Context(), "comparison.js", `
		import * as spectre from "spectre";
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
		"RPCThree":     `spectre.rpc("test.v1.Service.Get", () => true, "extra");`,
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := javascript.NewProgram(t.Context(), "comparison.js", `
				import * as spectre from "spectre";
			`+call)

			assert.Error(t, err)
		})
	}
}

func TestEvaluatorProtectsArgumentsFromMutation(t *testing.T) {
	program, err := javascript.NewProgram(t.Context(), "comparison.js", `
		import * as spectre from "spectre";
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
	program, err := javascript.NewProgram(t.Context(), "comparison.js", `
		import * as spectre from "spectre";
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

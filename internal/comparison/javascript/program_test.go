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
		spectre.field("test.v1.Response.value", (reference, candidate) =>
			reference === undefined && candidate === null);
	`)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()

	matched, err := evaluator.CompareField("test.v1.Response.value", nil, false, nil, true)

	assert.NoError(t, err)
	assert.True(t, matched)
}

func TestDeepEqualComparesNestedValues(t *testing.T) {
	program, err := javascript.NewProgram(t.Context(), "comparison.js", `
		import * as spectre from "spectre";
		spectre.field("test.v1.Response.value", spectre.deepEqual);
	`)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	tests := map[string]struct {
		reference any
		candidate any
		matched   bool
	}{
		"EqualObjects": {
			reference: map[string]any{"nested": []any{"one", float64(2)}},
			candidate: map[string]any{"nested": []any{"one", float64(2)}},
			matched:   true,
		},
		"DifferentObjects": {
			reference: map[string]any{"nested": []any{"one", float64(2)}},
			candidate: map[string]any{"nested": []any{"one", float64(3)}},
		},
		"ArrayOrderMatters": {
			reference: []any{"one", "two"},
			candidate: []any{"two", "one"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			matched, err := evaluator.CompareField(
				"test.v1.Response.value",
				test.reference,
				true,
				test.candidate,
				true,
			)

			assert.NoError(t, err)
			assert.Equal(t, test.matched, matched)
		})
	}
}

func TestDeepEqualRequiresTwoArguments(t *testing.T) {
	tests := map[string]string{
		"Missing": `spectre.deepEqual();`,
		"One":     `spectre.deepEqual("one");`,
		"Three":   `spectre.deepEqual("one", "two", "three");`,
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
		spectre.message("test.v1.Response", (reference, candidate) => {
			reference.value = "changed";
			candidate.value = "changed";
			return true;
		});
	`)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	reference := map[string]any{"value": "original"}
	candidate := map[string]any{"value": "original"}

	matched, err := evaluator.CompareMessage("test.v1.Response", reference, true, candidate, true)

	assert.NoError(t, err)
	assert.True(t, matched)
	assert.Equal(t, map[string]any{"value": "original"}, reference)
	assert.Equal(t, map[string]any{"value": "original"}, candidate)
}

func TestEvaluatorsKeepConcurrentRuntimesIsolated(t *testing.T) {
	program, err := javascript.NewProgram(t.Context(), "comparison.js", `
		import * as spectre from "spectre";
		spectre.field("test.v1.Response.value", (reference, candidate) => reference === candidate);
	`)
	assert.NoError(t, err)

	const evaluatorCount = 8
	type result struct {
		matched bool
		err     error
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
			matched, err := evaluator.CompareField("test.v1.Response.value", "same", true, "same", true)
			results <- result{matched: matched, err: err}
		})
	}
	wait.Wait()
	close(results)

	for result := range results {
		assert.NoError(t, result.err)
		assert.True(t, result.matched)
	}
}

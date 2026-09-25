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

package comparisoninternal

import (
	"context"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/schema"
)

// normalisationRun owns the mutable document and isolated evaluator for one payload.
// It is single-use because normalisers destructively rewrite the document.
type normalisationRun struct {
	loaded    *schema.Schema
	evaluator *javascript.Evaluator
	root      *schema.Type
	targets   []normalisationTarget
	side      string
	payload   *document
}

func newNormalisationRun(
	loaded *schema.Schema,
	evaluator *javascript.Evaluator,
	root *schema.Type,
	targets []normalisationTarget,
	side string,
	payload *document,
) *normalisationRun {
	return &normalisationRun{
		loaded:    loaded,
		evaluator: evaluator,
		root:      root,
		targets:   targets,
		side:      side,
		payload:   payload,
	}
}

// normalise logs a summary for every payload, so a payload with no applicable
// normalisers is still visible in the logs.
func (r *normalisationRun) normalise(ctx context.Context) error {
	occurrences := collectOccurrences(r.loaded, r.root, r.targets, r.payload)
	for _, occurrence := range occurrences {
		// A removed value still reaches later normalisers at its path as undefined.
		value := occurrence.value(r.payload)
		normalised, err := occurrence.normalise(r.evaluator, value)
		if err != nil {
			r.logResult(ctx, occurrence, err.Error())
			return err
		}
		r.logResult(ctx, occurrence, "")
		if err := occurrence.apply(r.payload, normalised); err != nil {
			return err
		}
	}
	logger.FromContext(ctx).DebugContext(ctx, "Payload normalisation completed",
		"message", r.root.Name,
		"side", r.side,
		"normalisers", len(occurrences),
	)
	return nil
}

func (r *normalisationRun) logResult(ctx context.Context, occurrence occurrence, reason string) {
	attributes := occurrence.logAttributes(r.side)
	if reason != "" {
		attributes = append(attributes, "outcome", "unable", "reason", reason)
	}
	logger.FromContext(ctx).DebugContext(ctx, "Payload normaliser completed", attributes...)
}

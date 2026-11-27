package comparisoninternal

import (
	"context"
	"log/slog"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// normalisationRun owns the mutable document for one payload, and starts an isolated
// evaluator only once a normaliser applies. It is single-use because normalisers rewrite the document.
type normalisationRun struct {
	evaluator func() (*javascript.Evaluator, error)
	root      *schema.ObjectType
	targets   []normalisationTarget
	side      model.Side
	payload   *document
}

func newNormalisationRun(
	evaluator func() (*javascript.Evaluator, error),
	root *schema.ObjectType,
	targets []normalisationTarget,
	side model.Side,
	payload *document,
) *normalisationRun {
	return &normalisationRun{
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
	occurrences := collectOccurrences(r.root, r.targets, r.payload)
	for _, occurrence := range occurrences {
		evaluator, err := r.evaluator()
		if err != nil {
			return err
		}
		// A removed value still reaches later normalisers at its path as undefined.
		value := occurrence.value(r.payload)
		normalised, err := occurrence.normalise(evaluator, value)
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
		attributes = append(attributes, slog.String("outcome", "unable"), slog.String("reason", reason))
	}
	logger.FromContext(ctx).LogAttrs(ctx, slog.LevelDebug, "Payload normaliser completed", attributes...)
}

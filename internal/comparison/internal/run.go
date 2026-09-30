package comparisoninternal

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/block/spectre/internal/comparison/javascript"
)

// normalisationRun owns the mutable document and isolated evaluator for one payload.
// It is single-use because normalisers destructively rewrite the document.
type normalisationRun struct {
	log       *slog.Logger
	evaluator *javascript.Evaluator
	root      protoreflect.MessageDescriptor
	targets   []normalisationTarget
	side      string
	payload   *document
}

func newNormalisationRun(
	log *slog.Logger,
	evaluator *javascript.Evaluator,
	root protoreflect.MessageDescriptor,
	targets []normalisationTarget,
	side string,
	payload *document,
) *normalisationRun {
	return &normalisationRun{
		log:       log,
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
	r.log.DebugContext(ctx, "Payload normalisation completed",
		"message", string(r.root.FullName()),
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
	r.log.DebugContext(ctx, "Payload normaliser completed", attributes...)
}

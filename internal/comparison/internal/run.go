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
	method    protoreflect.MethodDescriptor
	targets   []normalisationTarget
	side      string
	payload   *document
}

func newNormalisationRun(
	log *slog.Logger,
	evaluator *javascript.Evaluator,
	method protoreflect.MethodDescriptor,
	targets []normalisationTarget,
	side string,
	payload *document,
) *normalisationRun {
	return &normalisationRun{
		log:       log,
		evaluator: evaluator,
		method:    method,
		targets:   targets,
		side:      side,
		payload:   payload,
	}
}

func (r *normalisationRun) normalise(ctx context.Context) error {
	for _, occurrence := range collectOccurrences(r.method, r.targets, r.payload) {
		value := occurrence.value(r.payload)
		if occurrence.shouldSkip(value) {
			continue
		}
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
	return nil
}

func (r *normalisationRun) logResult(ctx context.Context, occurrence occurrence, reason string) {
	attributes := occurrence.logAttributes(r.side)
	if reason != "" {
		attributes = append(attributes, "outcome", "unable", "reason", reason)
	}
	r.log.DebugContext(ctx, "Response normaliser completed", attributes...)
}

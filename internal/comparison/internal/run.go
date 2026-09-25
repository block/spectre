package comparisoninternal

import (
	"context"
	"log/slog"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/block/spectre/internal/comparison/javascript"
)

// comparisonRun owns the mutable documents and isolated evaluator for one response pair.
// It is single-use because successful comparisons destructively prune both documents.
type comparisonRun struct {
	log       *slog.Logger
	evaluator *javascript.Evaluator
	method    protoreflect.MethodDescriptor
	targets   []comparisonTarget
	reference *document
	candidate *document
}

func newComparisonRun(
	log *slog.Logger,
	evaluator *javascript.Evaluator,
	method protoreflect.MethodDescriptor,
	targets []comparisonTarget,
	referenceJSON, candidateJSON []byte,
) (*comparisonRun, error) {
	reference, err := newDocument(referenceJSON)
	if err != nil {
		return nil, errors.Wrap(err, "parse reference comparison JSON")
	}
	candidate, err := newDocument(candidateJSON)
	if err != nil {
		return nil, errors.Wrap(err, "parse candidate comparison JSON")
	}
	return &comparisonRun{
		log:       log,
		evaluator: evaluator,
		method:    method,
		targets:   targets,
		reference: reference,
		candidate: candidate,
	}, nil
}

func (r *comparisonRun) compare(ctx context.Context) (differences []string, err error) {
	differences = []string{}
	for _, occurrence := range collectOccurrences(r.method, r.targets, r.reference, r.candidate) {
		reference, candidate := occurrence.values(r.reference, r.candidate)
		if occurrence.shouldSkip(reference, candidate) {
			continue
		}
		matched, err := occurrence.compare(r.evaluator, reference, candidate)
		if err != nil {
			r.logResult(ctx, occurrence, false, err.Error())
			return nil, err
		}
		r.logResult(ctx, occurrence, matched, "")
		if !matched {
			differences = appendDifference(differences, occurrence.difference())
			continue
		}
		if err := occurrence.delete(r.reference, r.candidate); err != nil {
			return nil, err
		}
	}
	differences = diffValues(r.reference.Export(), r.candidate.Export(), newDocumentPath(nil), differences)
	return differences, nil
}

func (r *comparisonRun) logResult(ctx context.Context, occurrence occurrence, matched bool, reason string) {
	attributes := occurrence.logAttributes()
	if reason == "" {
		attributes = append(attributes, "matched", matched)
	} else {
		attributes = append(attributes, "outcome", "unable", "reason", reason)
	}
	r.log.DebugContext(ctx, "Response comparator completed", attributes...)
}

package comparisoninternal

import (
	"context"
	"log/slog"
	"slices"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/schema"
)

// Plan binds JavaScript comparator declarations to one reflected schema.
// It is immutable after construction and safe to share across comparisons.
type Plan struct {
	loaded   *schema.Schema
	resolved []comparisonTarget
}

// NewPlan resolves and validates every declared comparator target before activation.
func NewPlan(loaded *schema.Schema, fields, messages, rpcs []string) (*Plan, error) {
	targets := make([]comparisonTarget, 0, len(fields)+len(messages)+len(rpcs))
	for _, declared := range []struct {
		kind  targetKind
		names []string
	}{
		{kind: targetField, names: fields},
		{kind: targetMessage, names: messages},
		{kind: targetRPC, names: rpcs},
	} {
		for _, name := range declared.names {
			resolved, err := resolveTarget(loaded, declared.kind, name)
			if err != nil {
				return nil, errors.Wrapf(err, "validate comparator target %q", name)
			}
			targets = append(targets, resolved)
		}
	}
	return &Plan{loaded: loaded, resolved: targets}, nil
}

// Schema returns the descriptor schema bound to the plan.
func (p *Plan) Schema() *schema.Schema {
	return p.loaded
}

// Compare applies the plan to one response pair using the request's evaluator.
func (p *Plan) Compare(
	ctx context.Context,
	log *slog.Logger,
	evaluator *javascript.Evaluator,
	method protoreflect.MethodDescriptor,
	runComparators bool,
	referenceJSON, candidateJSON []byte,
) (differences []string, err error) {
	targets := p.resolved
	if !runComparators {
		targets = nil
	}
	comparison, err := newComparisonRun(log, evaluator, method, slices.Clone(targets), referenceJSON, candidateJSON)
	if err != nil {
		return nil, err
	}
	return comparison.compare(ctx)
}

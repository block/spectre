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

// Plan binds JavaScript normaliser declarations to one reflected schema.
// It is immutable after construction and safe to share across normalisations.
type Plan struct {
	loaded   *schema.Schema
	resolved []normalisationTarget
}

// NewPlan resolves and validates every declared normaliser target before activation.
func NewPlan(loaded *schema.Schema, fields, messages []string) (*Plan, error) {
	targets := make([]normalisationTarget, 0, len(fields)+len(messages))
	for _, declared := range []struct {
		kind  targetKind
		names []string
	}{
		{kind: targetField, names: fields},
		{kind: targetMessage, names: messages},
	} {
		for _, name := range declared.names {
			resolved, err := resolveTarget(loaded, declared.kind, name)
			if err != nil {
				return nil, errors.Wrapf(err, "validate normaliser target %q", name)
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

// Normalise applies the plan to one payload of type root, using an evaluator owned
// by that payload. side only labels log records.
func (p *Plan) Normalise(
	ctx context.Context,
	log *slog.Logger,
	evaluator *javascript.Evaluator,
	root protoreflect.MessageDescriptor,
	runNormalisers bool,
	side string,
	payloadJSON []byte,
) (*Normalised, error) {
	payload, err := newDocument(payloadJSON)
	if err != nil {
		return nil, errors.Wrapf(err, "parse %s payload JSON", side)
	}
	targets := p.resolved
	if !runNormalisers {
		targets = nil
	}
	run := newNormalisationRun(log, evaluator, root, slices.Clone(targets), side, payload)
	if err := run.normalise(ctx); err != nil {
		return nil, err
	}
	return newNormalised(payload), nil
}

// Normalised is one payload after every applicable normaliser has run.
type Normalised struct {
	payload *document
}

func newNormalised(payload *document) *Normalised {
	return &Normalised{payload: payload}
}

func (n *Normalised) document() *document {
	return n.payload
}

// Diff returns the paths at which two normalised payloads differ, without values.
func Diff(reference, candidate *Normalised) (differences []string) {
	root := newDocumentPath(nil)
	referenceRoot := reference.document().Value(root)
	candidateRoot := candidate.document().Value(root)
	if referenceRoot.isPresent() != candidateRoot.isPresent() {
		return []string{root.String()}
	}
	return diffValues(reference.document().Export(), candidate.document().Export(), root, []string{})
}

package comparisoninternal

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// Plan binds JavaScript normaliser declarations to one declared schema.
// It is immutable after construction and safe to share across normalisations.
type Plan struct {
	types    *normalisedTypes
	resolved []normalisationTarget
}

// NewPlan resolves and validates every declared normaliser target before activation.
func NewPlan(loaded *schema.Schema, fields []javascript.FieldTarget, messages []javascript.MessageTarget) (*Plan, error) {
	types, err := newNormalisedTypes(loaded, messages)
	if err != nil {
		return nil, err
	}
	targets := make([]normalisationTarget, 0, len(fields)+len(messages))
	for _, declared := range fields {
		root, err := types.fieldRoot(declared.Type())
		if err != nil {
			return nil, errors.Wrapf(err, "validate normaliser target %q", declared.String())
		}
		steps, err := resolveField(root, declared.Path())
		if err != nil {
			return nil, errors.Wrapf(err, "validate normaliser target %q", declared.String())
		}
		targets = append(targets, newFieldTarget(declared, steps))
	}
	for _, message := range messages {
		if !message.Normalises() {
			continue
		}
		incoming, err := loaded.Type(message.Type())
		if err != nil {
			return nil, errors.Wrapf(err, "validate normaliser target %q", message.Type())
		}
		targets = append(targets, newMessageTarget(incoming.Name, types.of(incoming)))
	}
	return &Plan{types: types, resolved: targets}, nil
}

// Normalise gives a copy of one payload of type root its normalised type, then applies the plan.
// evaluator is called only once a normaliser applies, and the caller closes what it returns. side only labels log records.
func (p *Plan) Normalise(
	ctx context.Context,
	evaluator func() (*javascript.Evaluator, error),
	root *schema.ObjectType,
	side string,
	payload schema.Value,
) (*Normalised, error) {
	if normalised := p.types.slot(payload.Type()); !sameType(normalised, payload.Type()) {
		// Every value of a type is a value of its normalised type, which only relaxes it.
		retyped, err := schema.As(normalised, payload)
		if err != nil {
			return nil, errors.Wrap(err, "give the payload its normalised type")
		}
		payload = retyped
	}
	document := newDocument(payload)
	run := newNormalisationRun(evaluator, p.types.of(root), slices.Clone(p.resolved), side, document)
	if err := run.normalise(ctx); err != nil {
		return nil, err
	}
	return newNormalised(document), nil
}

// Normalised is one payload after every applicable normaliser has run. Normalisers
// may remove the root or change its members, but every value keeps its normalised type.
type Normalised struct {
	payload *document
}

func newNormalised(payload *document) *Normalised {
	return &Normalised{payload: payload}
}

// Raw wraps a payload that no normaliser applies to.
func Raw(payload schema.Value) *Normalised {
	return newNormalised(newDocument(payload))
}

func (n *Normalised) document() *document {
	return n.payload
}

// CanonicalJSON encodes the payload with sorted object keys, so structurally equal
// payloads encode identically. A removed root encodes as null.
func (n *Normalised) CanonicalJSON() ([]byte, error) {
	data, err := json.Marshal(n.document().Export())
	return data, errors.Wrap(err, "encode canonical JSON")
}

// Diff returns where and how two normalised payloads differ, without values, and
// whether there were more differences. Both are instances of the reference's type.
func Diff(reference, candidate *Normalised) (differences []model.Difference, truncated bool) {
	return newDiffer().compare(reference.document().Export(), candidate.document().Export())
}

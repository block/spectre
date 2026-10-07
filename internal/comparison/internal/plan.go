package comparisoninternal

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/schema"
)

// Plan binds JavaScript normaliser declarations to one declared schema.
// It is immutable after construction and safe to share across normalisations.
type Plan struct {
	loaded   *schema.Schema
	resolved []normalisationTarget
}

// NewPlan resolves and validates every declared normaliser target before activation.
func NewPlan(loaded *schema.Schema, fields []javascript.FieldTarget, messages []string) (*Plan, error) {
	targets := make([]normalisationTarget, 0, len(fields)+len(messages))
	for _, declared := range fields {
		root, steps, err := resolveField(loaded, declared)
		if err != nil {
			return nil, errors.Wrapf(err, "validate normaliser target %q", declared.String())
		}
		targets = append(targets, newFieldTarget(declared, root, steps))
	}
	for _, name := range messages {
		root, err := loaded.Type(name)
		if err != nil {
			return nil, errors.Wrapf(err, "validate normaliser target %q", name)
		}
		targets = append(targets, newMessageTarget(name, root))
	}
	return &Plan{loaded: loaded, resolved: targets}, nil
}

// Schema returns the declared schema bound to the plan.
func (p *Plan) Schema() *schema.Schema {
	return p.loaded
}

// Normalise applies the plan to one decoded JSON payload of type root, which it takes
// ownership of and mutates. side only labels log records.
func (p *Plan) Normalise(
	ctx context.Context,
	log *slog.Logger,
	evaluator *javascript.Evaluator,
	root *schema.Type,
	runNormalisers bool,
	side string,
	payload any,
) (*Normalised, error) {
	document := newDocument(payload)
	targets := p.resolved
	if !runNormalisers {
		targets = nil
	}
	run := newNormalisationRun(p.loaded, log, evaluator, root, slices.Clone(targets), side, document)
	if err := run.normalise(ctx); err != nil {
		return nil, err
	}
	return newNormalised(document), nil
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

// CanonicalJSON encodes the payload with sorted object keys, so structurally equal
// payloads encode identically. A removed root encodes as null.
func (n *Normalised) CanonicalJSON() ([]byte, error) {
	data, err := json.Marshal(n.document().Export())
	return data, errors.Wrap(err, "encode canonical JSON")
}

// Diff returns the paths at which two normalised payloads differ, without values.
func Diff(reference, candidate *Normalised) (differences []string) {
	root := newDocumentPath(nil)
	referenceRoot, referencePresent := reference.document().Export().Get()
	candidateRoot, candidatePresent := candidate.document().Export().Get()
	if referencePresent != candidatePresent {
		return []string{root.String()}
	}
	return diffValues(referenceRoot, candidateRoot, root, []string{})
}

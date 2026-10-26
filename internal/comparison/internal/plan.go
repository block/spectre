package comparisoninternal

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

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
func NewPlan(loaded *schema.Schema, fields []javascript.FieldTarget, messages []schema.TypeRef) (*Plan, error) {
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
	evaluator *javascript.Evaluator,
	root *schema.Type,
	side string,
	payload any,
) (*Normalised, error) {
	document := newDocument(payload)
	run := newNormalisationRun(p.loaded, evaluator, root, slices.Clone(p.resolved), side, document)
	if err := run.normalise(ctx); err != nil {
		return nil, err
	}
	rootValue := schema.Value{Kind: schema.KindObject, Type: root.Name}
	return newNormalised(document, Some(p.loaded), Some(rootValue)), nil
}

// Normalised is one payload after every applicable normaliser has run.
type Normalised struct {
	payload *document
	// loaded and root are absent for an untyped payload, whose keys are all payload data.
	loaded Option[*schema.Schema]
	root   Option[schema.Value]
}

func newNormalised(payload *document, loaded Option[*schema.Schema], root Option[schema.Value]) *Normalised {
	return &Normalised{payload: payload, loaded: loaded, root: root}
}

// Raw wraps an untyped payload that no normaliser applies to.
func Raw(payload any) *Normalised {
	return newNormalised(newDocument(payload), None[*schema.Schema](), None[schema.Value]())
}

// differ compares against this payload's declared type.
func (n *Normalised) differ() *differ {
	return newDiffer(n.loaded, n.root)
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
// Both payloads are instances of the reference's declared type.
func Diff(reference, candidate *Normalised) (differences []string) {
	root := newDocumentPath(nil)
	referenceRoot, referencePresent := reference.document().Export().Get()
	candidateRoot, candidatePresent := candidate.document().Export().Get()
	if referencePresent != candidatePresent {
		return []string{root.String()}
	}
	return reference.differ().compare(referenceRoot, candidateRoot, root)
}

// Package comparison normalises payloads with scripts, then diffs or hashes them.
package comparison

import (
	"crypto/sha256"

	"github.com/alecthomas/errors"

	comparisoninternal "github.com/block/spectre/internal/comparison/internal"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// Hash identifies a payload by its identity and normalised content, so
// equivalent payloads share a hash.
type Hash [sha256.Size]byte

// Normalised is a payload after its normalisers ran, kept so that Diff can compare
// it later. declared is the type of the payload before they ran.
type Normalised struct {
	declared schema.Type
	payload  *comparisoninternal.Normalised
}

func newNormalised(declared schema.Type, payload *comparisoninternal.Normalised) Normalised {
	return Normalised{declared: declared, payload: payload}
}

// Diff returns where candidate differs from the reference n, and whether it differs in
// more places. Both must have been instances of the same type.
func (n Normalised) Diff(candidate Normalised) (differences []model.Difference, truncated bool, err error) {
	if !n.declared.Equal(candidate.declared) {
		return nil, false, errors.Errorf("reference is %s but candidate is %s", n.declared, candidate.declared)
	}
	differences, truncated = comparisoninternal.Diff(n.payload, candidate.payload)
	return differences, truncated, nil
}

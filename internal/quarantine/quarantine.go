// Package quarantine describes why a candidate was quarantined. A Reason never
// carries payload values, so it may leave the mirror.
package quarantine

import (
	"strings"

	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/boundary"
)

// Kind is why the candidate was quarantined.
type Kind string

const (
	// KindDivergent means the candidate's responses differ from the reference's.
	KindDivergent Kind = "divergent"
	// KindUnable means the mirror could not compare or replay traffic.
	KindUnable Kind = "unable"
	// KindUnmatched means the candidate made a dependency request the reference did not.
	KindUnmatched Kind = "unmatched"
	// KindUnlisted means an ingress route is in no node of the migration state.
	KindUnlisted Kind = "unlisted"
	// KindCapacity means candidate work exceeded a concurrency or buffer limit.
	KindCapacity Kind = "capacity"
	// KindPairing means the mirror could not pair candidate and reference traffic.
	KindPairing Kind = "pairing"
)

// Traffic identifies the request that triggered quarantine.
type Traffic struct {
	Direction boundary.Direction
	Protocol  boundary.Protocol
	Route     boundary.Route
}

// Reason is why the candidate was quarantined. Detail and Differences must never
// quote payload values, because reasons are reported outside the mirror.
type Reason struct {
	Kind    Kind
	Traffic Option[Traffic]
	// Node is the migration state node that owns an ingress route.
	Node Option[string]
	// Differences are the field paths of a divergence, without values.
	Differences []string
	Detail      string
}

// String describes the reason for logs.
func (r Reason) String() string {
	if r.Kind == KindDivergent {
		return "response differences: " + strings.Join(r.Differences, ", ")
	}
	return r.Detail
}

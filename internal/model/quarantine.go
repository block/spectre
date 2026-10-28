package model

import (
	"strings"
	"time"

	. "github.com/alecthomas/types/optional"
)

// QuarantineKind is why the candidate was quarantined.
type QuarantineKind string

const (
	// QuarantineDivergent means the candidate's responses differ from the reference's.
	QuarantineDivergent QuarantineKind = "divergent"
	// QuarantineUnable means the mirror could not compare or replay traffic.
	QuarantineUnable QuarantineKind = "unable"
	// QuarantineUnmatched means the candidate made a dependency request the reference did not.
	QuarantineUnmatched QuarantineKind = "unmatched"
	// QuarantineUnlisted means an ingress route is in no node of the migration state.
	QuarantineUnlisted QuarantineKind = "unlisted"
	// QuarantineCapacity means candidate work exceeded a concurrency or buffer limit.
	QuarantineCapacity QuarantineKind = "capacity"
	// QuarantinePairing means the mirror could not pair candidate and reference traffic.
	QuarantinePairing QuarantineKind = "pairing"
)

// QuarantineReason is why the candidate was quarantined. Detail and Differences
// must never quote payload values, because reasons are reported outside the mirror.
type QuarantineReason struct {
	Kind QuarantineKind `json:"kind"`
	// Traffic is the request that triggered quarantine.
	Traffic Option[Traffic] `json:"traffic,omitzero"`
	// Node is the migration state node that owns an ingress route.
	Node Option[string] `json:"node,omitzero"`
	// Differences are the field paths of a divergence, without values.
	Differences []string `json:"differences,omitempty"`
	Detail      string   `json:"detail,omitempty"`
}

// String describes the reason for logs.
func (r QuarantineReason) String() string {
	if r.Kind == QuarantineDivergent {
		return "response differences: " + strings.Join(r.Differences, ", ")
	}
	return r.Detail
}

// Quarantine is a quarantine in effect: why it began and when.
type Quarantine struct { //nolint:singleusestruct // Proxies report quarantines on their own, outside any divergence.
	Reason QuarantineReason `json:"reason"` //nolint:narrowdeps // Other packages read the whole reason.
	Since  time.Time        `json:"since"`
}

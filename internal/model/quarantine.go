package model

import (
	"strings"
	"time"

	. "github.com/alecthomas/types/optional"
)

// QuarantineKind is why the candidate was quarantined.
type QuarantineKind string

const (
	// QuarantineDivergent means the candidate's responses, or an egress request, differ from the reference's.
	QuarantineDivergent QuarantineKind = "divergent"
	// QuarantineUnable means the mirror could not compare or replay traffic.
	QuarantineUnable QuarantineKind = "unable"
	// QuarantineUnmatched means the candidate or reference made a dependency request the other did not.
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
	// Differences are where and how the payloads of a divergence differ, without values.
	Differences []Difference `json:"differences,omitempty"`
	// Truncated means the payloads had more differences than are listed.
	Truncated bool   `json:"truncated,omitzero"`
	Detail    string `json:"detail,omitempty"`
}

// String describes the reason for logs.
func (r QuarantineReason) String() string {
	if r.Kind == QuarantineDivergent {
		return r.Compared() + ": " + strings.Join(DifferenceLines(r.Differences, r.Truncated), ", ")
	}
	return r.Detail
}

// Compared names what the differences of a divergence are between: the requests
// of an egress call, or else the responses to an ingress request.
func (r QuarantineReason) Compared() string {
	if traffic, ok := r.Traffic.Get(); ok && traffic.Direction == Egress {
		return "request differences"
	}
	return "response differences"
}

// Quarantine is a quarantine in effect: why it began and when.
type Quarantine struct { //nolint:singleusestruct // Proxies report quarantines on their own, outside any state.
	Reason QuarantineReason `json:"reason"` //nolint:narrowdeps // Other packages read the whole reason.
	Since  time.Time        `json:"since"`
}

// SupersededBy reports whether a later recorded quarantine or a clear at or
// after this quarantine began prevents it from being recorded.
func (q Quarantine) SupersededBy(stored Option[Quarantine], cleared Option[time.Time]) bool {
	if existing, ok := stored.Get(); ok && existing.Since.After(q.Since) {
		return true
	}
	at, ok := cleared.Get()
	return ok && !q.Since.After(at)
}

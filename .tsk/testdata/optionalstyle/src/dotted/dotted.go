package dotted

import . "github.com/alecthomas/types/optional"

type Payload struct {
	Zero    Option[int]    `json:"zero,omitzero"`
	Empty   Option[int]    `json:"empty,omitempty"` // want `Empty is an Option, so tag it omitzero, not omitempty`
	Both    Option[string] `json:",omitempty,omitzero"` // want `Both is an Option, so tag it omitzero, not omitempty`
	Kept    Option[int]    `json:"kept,omitempty"` //nolint:optionalstyle kept for the test
	Untagged Option[int]
	Slice   []int `json:"slice,omitempty"`
	Named   string `json:"omitempty"`
}

package model

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// Change is how the candidate's value at a path differs from the reference's.
type Change string

const (
	// ChangeMissing means the reference has a value at the path and the candidate does not.
	ChangeMissing Change = "missing"
	// ChangeAdded means the candidate has a value at the path and the reference does not.
	ChangeAdded Change = "added"
	// ChangeChanged means both have values of the same JSON type that differ.
	ChangeChanged Change = "changed"
	// ChangeRetyped means both have values, of different JSON types.
	ChangeRetyped Change = "retyped"
)

// mark is the change's one-character symbol, as in a diff.
func (c Change) mark() string {
	switch c {
	case ChangeMissing:
		return "-"
	case ChangeAdded:
		return "+"
	case ChangeChanged:
		return "~"
	case ChangeRetyped:
		return "!"
	}
	return "?"
}

// JSONType is the type of a JSON value.
type JSONType string

// JSON types. A value's type never reveals the value itself.
const (
	JSONNull    JSONType = "null"
	JSONBoolean JSONType = "boolean"
	JSONNumber  JSONType = "number"
	JSONString  JSONType = "string"
	JSONObject  JSONType = "object"
	JSONList    JSONType = "list"
)

// Retype is the JSON types of a value whose type differs between the responses.
type Retype struct {
	Reference JSONType `json:"reference"`
	Candidate JSONType `json:"candidate"`
}

// UnmarshalJSON rejects unknown fields. Option decodes with json.Unmarshal, so
// a difference's retype must enforce strict decoding itself.
func (r *Retype) UnmarshalJSON(data []byte) error {
	type plain Retype
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return errors.WithStack(decoder.Decode((*plain)(r)))
}

// Difference is where and how a candidate's response differs from the
// reference's. It names only schema fields and JSON types, never values.
type Difference struct {
	// Path names declared fields; payload keys and merged list indexes are [*].
	Path   string `json:"path"`
	Change Change `json:"change"`
	// Retype is present only when Change is ChangeRetyped.
	Retype Option[Retype] `json:"retype,omitzero"`
	// Count is how many keys or list elements the path stands for.
	Count int `json:"count"`
}

// String writes the difference as a diff line, e.g. "! $.currency string → null".
func (d Difference) String() string {
	text := d.Change.mark() + " " + d.Path
	if retype, ok := d.Retype.Get(); ok {
		text += fmt.Sprintf(" %s → %s", retype.Reference, retype.Candidate)
	}
	if d.Count > 1 {
		text += fmt.Sprintf(" ×%d", d.Count)
	}
	return text
}

// DifferenceLines writes differences as diff lines, ending with "… more" when
// they were truncated.
func DifferenceLines(differences []Difference, truncated bool) []string {
	lines := make([]string, 0, len(differences)+1)
	for _, difference := range differences {
		lines = append(lines, difference.String())
	}
	if truncated {
		lines = append(lines, "… more")
	}
	return lines
}

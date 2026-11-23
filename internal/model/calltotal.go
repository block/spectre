package model

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/alecthomas/errors"
)

// CallTotal counts one service environment's compared calls across mirror runs.
type CallTotal struct {
	Service     string     `json:"service"`
	Environment string     `json:"environment"`
	Since       time.Time  `json:"since"`
	Start       TotalStart `json:"start"`
	Calls       Calls      `json:"calls,omitzero"`
	Runs        int        `json:"runs,omitzero"`
}

// UnmarshalJSON rejects unknown fields even when the total is held in an Option.
func (c *CallTotal) UnmarshalJSON(data []byte) error {
	type plain CallTotal
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return errors.Wrap(decoder.Decode((*plain)(c)), "decode call total")
}

// TotalStart says what began a counting period.
type TotalStart string

const (
	// TotalFirstReport begins the first counting period.
	TotalFirstReport TotalStart = "first report"
	// TotalQuarantine begins a period after a new quarantine.
	TotalQuarantine TotalStart = "last quarantine"
	// TotalClear begins a period when divergences are cleared.
	TotalClear TotalStart = "cleared"
)

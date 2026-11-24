// Package model is SPECTRE's data model: the traffic that crosses a service's
// boundary through its proxies, and why a candidate was quarantined.
package model

import (
	"bytes"
	"encoding/json"

	"github.com/alecthomas/errors"
)

// Direction names the proxy that carries traffic: ingress into the service, or
// egress from it to a dependency.
type Direction string

const (
	// Ingress traffic is requests to the proxied service.
	Ingress Direction = "ingress"
	// Egress traffic is requests from the service to a dependency.
	Egress Direction = "egress"
)

// Protocol names the protocol that a proxy intercepts, e.g. "http".
type Protocol string

// Route is a protocol's canonical request route, e.g. "GET forecasts.example/v1/forecasts/{location}".
// Equal routes must have identical text, because egress pairing hashes it.
type Route string

// Traffic identifies one request route of a kind of IO.
type Traffic struct {
	Direction Direction `json:"direction"`
	Protocol  Protocol  `json:"protocol"`
	Route     Route     `json:"route"`
}

// UnmarshalJSON rejects unknown fields. Option decodes with json.Unmarshal, so
// a reason's traffic must enforce strict decoding itself.
func (t *Traffic) UnmarshalJSON(data []byte) error {
	type plain Traffic
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return errors.WithStack(decoder.Decode((*plain)(t)))
}

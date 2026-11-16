package model

import "maps"

// Calls counts the requests on each route whose responses the ingress proxy
// compared since the mirror started.
type Calls struct {
	Routes map[Route]uint64 `json:"routes,omitempty"`
	// Other counts requests to routes beyond the most a mirror counts.
	Other uint64 `json:"other,omitzero"`
}

func (Calls) update() {}

// Equal reports whether both count the same requests.
func (c Calls) Equal(other Calls) bool {
	return c.Other == other.Other && maps.Equal(c.Routes, other.Routes)
}

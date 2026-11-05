package model

import "maps"

// Calls counts the requests the ingress proxy received on each route since the
// mirror started.
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

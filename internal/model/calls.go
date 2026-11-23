package model

import "maps"

// Calls counts the requests on each route whose responses the ingress proxy
// compared.
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

// IsZero reports whether c counts no requests.
func (c Calls) IsZero() bool {
	if c.Other > 0 {
		return false
	}
	for _, count := range c.Routes {
		if count > 0 {
			return false
		}
	}
	return true
}

// Plus returns the requests both count together.
func (c Calls) Plus(other Calls) Calls {
	sum := Calls{Routes: make(map[Route]uint64, len(c.Routes)), Other: c.Other + other.Other}
	for _, routes := range []map[Route]uint64{c.Routes, other.Routes} {
		for route, count := range routes {
			if count > 0 {
				sum.Routes[route] += count
			}
		}
	}
	return sum
}

// Minus returns the requests c counts beyond earlier, which an earlier count
// of the same mirror run holds. Routes left with no requests are dropped.
func (c Calls) Minus(earlier Calls) Calls {
	difference := Calls{Routes: make(map[Route]uint64, len(c.Routes)), Other: c.Other - min(c.Other, earlier.Other)}
	for route, count := range c.Routes {
		if count > earlier.Routes[route] {
			difference.Routes[route] = count - earlier.Routes[route]
		}
	}
	return difference
}

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

// IsZero reports whether there are no compared calls.
func (c Calls) IsZero() bool {
	for _, count := range c.Routes {
		if count > 0 {
			return false
		}
	}
	return c.Other == 0
}

// Plus adds counts without changing either input.
func (c Calls) Plus(other Calls) Calls {
	if other.IsZero() {
		return c
	}
	sum := Calls{Other: c.Other + other.Other}
	for _, routes := range []map[Route]uint64{c.Routes, other.Routes} {
		for route, count := range routes {
			if count > 0 {
				if len(sum.Routes) == 0 {
					sum.Routes = map[Route]uint64{}
				}
				sum.Routes[route] += count
			}
		}
	}
	return sum
}

// Minus returns positive increases beyond an earlier cumulative count.
func (c Calls) Minus(earlier Calls) Calls {
	difference := Calls{Other: c.Other - min(c.Other, earlier.Other)}
	for route, count := range c.Routes {
		if count > earlier.Routes[route] {
			if len(difference.Routes) == 0 {
				difference.Routes = map[Route]uint64{}
			}
			difference.Routes[route] = count - earlier.Routes[route]
		}
	}
	return difference
}

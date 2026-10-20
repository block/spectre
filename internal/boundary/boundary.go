// Package boundary names the kinds of IO that cross a service's boundary, and
// so pass through SPECTRE's proxies.
package boundary

import (
	"cmp"
	"fmt"
	"slices"

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

// ProtocolHTTP is the protocol of HTTP servers, including gRPC and Connect.
const ProtocolHTTP Protocol = "http"

// Kind is one kind of IO that a proxy intercepts, written "ingress[http]".
type Kind struct {
	Direction Direction
	Protocol  Protocol
}

// String returns the kind as written in the migration state, e.g. "ingress[http]".
func (k Kind) String() string {
	return fmt.Sprintf("%s[%s]", k.Direction, k.Protocol)
}

// Support is a kind of IO that a protocol server proxies, with a description of
// the code that performs it and the settings a migration state node of it takes.
type Support struct {
	Kind        Kind
	Description string
	Settings    []Setting
}

// Setting is one field that a node's table for a kind of IO can hold. With a
// Key, it is a table of entries, each holding the Entry settings.
type Setting struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
	// Key describes what keys the entries, or is empty for a plain value.
	Key string `json:"key,omitzero"`
	// Unique keyed entries may belong to only one node, such as a route.
	Unique bool `json:"unique,omitzero"`
	// Entry lists the settings that each entry takes.
	Entry []Setting `json:"entry,omitzero"`
}

// Catalog is every kind of IO that the protocol servers in one binary support.
type Catalog struct {
	supports []Support
}

// NewCatalog collects what protocol servers support, grouped by protocol with
// ingress first, so listings are stable. A kind supported twice is an error.
func NewCatalog(supports ...Support) (*Catalog, error) {
	sorted := slices.Clone(supports)
	slices.SortFunc(sorted, func(a, b Support) int {
		return cmp.Or(cmp.Compare(a.Kind.Protocol, b.Kind.Protocol), directionOrder(a.Kind.Direction)-directionOrder(b.Kind.Direction))
	})
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Kind == sorted[i-1].Kind {
			return nil, errors.Errorf("%s is supported twice", sorted[i].Kind)
		}
	}
	return &Catalog{supports: sorted}, nil
}

// Supports returns every supported kind of IO in listing order.
func (c *Catalog) Supports() []Support {
	return slices.Clone(c.supports)
}

// Support returns what the catalog supports for kind, if it supports it.
func (c *Catalog) Support(kind Kind) (Support, bool) {
	for _, support := range c.supports {
		if support.Kind == kind {
			return support, true
		}
	}
	return Support{}, false
}

func directionOrder(direction Direction) int {
	if direction == Ingress {
		return 0
	}
	return 1
}

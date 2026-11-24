// Package state describes a service's migration state: the modules a migration
// moves, the IO each performs, and how far each has got.
package state

import (
	"fmt"

	"github.com/block/spectre/internal/model"
)

// Library is the node type of shared code that performs no IO.
const Library = "library"

// NodeType is a type that a node in the migration state can have.
type NodeType struct {
	// Name is how the type is referred to, e.g. "library" or "ingress[http]".
	Name string `json:"name"`
	// Description says what code has the type and how to recognise it.
	Description string `json:"description"`
	// Table is where a node of an IO kind holds its settings, or empty for library.
	Table string `json:"table,omitzero"`
	// Settings are the fields the table can hold.
	Settings []Setting `json:"settings,omitzero"`
}

// Setting describes one field that a node's table for a kind of IO can hold. With a
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

// Kind is one direction a protocol proxies, as the migration state describes it.
type Kind struct {
	Direction   model.Direction
	Protocol    model.Protocol
	Description string
	// Settings are what a node's table for the kind holds. A Unique setting's keys
	// are the routes the protocol names traffic by.
	Settings []Setting
}

// String returns the kind as written in the migration state, e.g. "ingress[http]".
func (k Kind) String() string {
	return k.key().String()
}

func (k Kind) key() kindKey {
	return kindKey{Direction: k.Direction, Protocol: k.Protocol}
}

// kindKey identifies a kind by direction and protocol, so it can key a map.
type kindKey struct {
	Direction model.Direction
	Protocol  model.Protocol
}

func (k kindKey) String() string {
	return fmt.Sprintf("%s[%s]", k.Direction, k.Protocol)
}

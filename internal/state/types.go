// Package state describes a service's migration state: the modules a migration
// moves, the IO each performs, and how far each has got.
package state

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

// NodeTypes describes library, then each kind of IO a node can have, matching the
// fields of Ingress and Egress.
func NodeTypes() []NodeType {
	return []NodeType{
		{
			Name: Library,
			Description: "Module shared by more than one other node that performs no IO, " +
				"such as shared domain types, validation, calculations, and helpers.",
		},
		{
			Name:        "ingress[http]",
			Description: "Module that receives HTTP requests to the service, including gRPC and Connect.",
			Table:       "nodes.<id>.ingress.http",
			Settings: []Setting{{
				Name:        "routes",
				Required:    true,
				Description: "Each route the node handles.",
				Key:         "<METHOD> /<path>",
				Unique:      true,
			}},
		},
		{
			Name:        "egress[http]",
			Description: "Module that sends HTTP requests to another service, including gRPC and Connect.",
			Table:       "nodes.<id>.egress.http",
		},
	}
}

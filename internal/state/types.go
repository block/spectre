// Package state describes a service's migration state: the modules a migration
// moves, the IO each performs, and how far each has got.
package state

import "fmt"

// Library is the node type of shared code that performs no IO.
const Library = "library"

const libraryDescription = "Module shared by more than one other node that performs no IO, " +
	"such as shared domain types, validation, calculations, and helpers."

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

// NodeTypes returns library, then each kind of IO the catalog supports.
func NodeTypes(catalog *Catalog) []NodeType {
	supports := catalog.Supports()
	types := make([]NodeType, 0, len(supports)+1)
	types = append(types, NodeType{Name: Library, Description: libraryDescription})
	for _, support := range supports {
		types = append(types, NodeType{
			Name:        support.Kind.String(),
			Description: support.Description,
			Table:       fmt.Sprintf("nodes.<id>.%s.%s", support.Kind.Direction, support.Kind.Protocol),
			Settings:    support.Settings,
		})
	}
	return types
}

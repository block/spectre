// Package state describes a service's migration state: the modules a migration
// moves, the IO each performs, and how far each has got.
package state

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/boundary"
)

// Library is the node type of shared code that performs no IO.
const Library = "library"

const libraryDescription = "Code shared by more than one other node that performs no IO, " +
	"such as shared domain types, validation, calculations, and helpers. " +
	"Code that only one node uses belongs to that node, not to a library. " +
	"Shared code that does any IO is not a library either; it is a node with the IO kinds it performs."

// TypesConfig selects how node types are printed.
type TypesConfig struct {
	// JSON prints the node types as a JSON array instead of Markdown.
	JSON bool `help:"Print JSON instead of Markdown."`
}

// NodeType is a type that a node in the migration state can have.
type NodeType struct {
	// Name is how the state file writes the type, e.g. "library" or "ingress[http]".
	Name string `json:"name"`
	// Description says what code has the type and how to recognise it.
	Description string `json:"description"`
}

// NodeTypes returns library, then each kind of IO the catalog supports.
func NodeTypes(catalog *boundary.Catalog) []NodeType {
	supports := catalog.Supports()
	types := make([]NodeType, 0, len(supports)+1)
	types = append(types, NodeType{Name: Library, Description: libraryDescription})
	for _, support := range supports {
		types = append(types, NodeType{Name: support.Kind.String(), Description: support.Description})
	}
	return types
}

// WriteTypes writes every node type with its description, for an agent that
// writes the migration state.
func WriteTypes(out io.Writer, config TypesConfig, catalog *boundary.Catalog) error {
	types := NodeTypes(catalog)
	if config.JSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return errors.Wrap(encoder.Encode(types), "write node types")
	}
	for i, nodeType := range types {
		separator := ""
		if i > 0 {
			separator = "\n"
		}
		if _, err := fmt.Fprintf(out, "%s## %s\n\n%s\n", separator, nodeType.Name, nodeType.Description); err != nil {
			return errors.Wrap(err, "write node types")
		}
	}
	return nil
}

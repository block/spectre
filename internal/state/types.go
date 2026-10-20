// Package state describes a service's migration state: the modules a migration
// moves, the IO each performs, and how far each has got.
package state

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/boundary"
)

// Library is the node type of shared code that performs no IO.
const Library = "library"

const libraryDescription = "Code shared by more than one other node that performs no IO, " +
	"such as shared domain types, validation, calculations, and helpers. " +
	"Code that only one node uses belongs to that node, not to a library. " +
	"Shared code that does any IO is not a library either; it is a node with the IO kinds it performs. " +
	"A library node has no IO kind tables."

// TypesConfig selects how node types are printed.
type TypesConfig struct {
	// JSON prints the node types as a JSON array instead of Markdown.
	JSON bool `help:"Print JSON instead of Markdown."`
}

// NodeType is a type that a node in the migration state can have.
type NodeType struct {
	// Name is how the type is referred to, e.g. "library" or "ingress[http]".
	Name string `json:"name"`
	// Description says what code has the type and how to recognise it.
	Description string `json:"description"`
	// Table is where a node of an IO kind holds its settings, or empty for library.
	Table string `json:"table,omitzero"`
	// Settings are the fields the table can hold.
	Settings []boundary.Setting `json:"settings,omitzero"`
}

// NodeTypes returns library, then each kind of IO the catalog supports.
func NodeTypes(catalog *boundary.Catalog) []NodeType {
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

// WriteTypes writes every node type with its description and settings, for an
// agent that writes the migration state.
func WriteTypes(out io.Writer, config TypesConfig, catalog *boundary.Catalog) error {
	types := NodeTypes(catalog)
	if config.JSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return errors.Wrap(encoder.Encode(types), "write node types")
	}
	sections := make([]string, 0, len(types))
	for _, nodeType := range types {
		sections = append(sections, markdown(nodeType))
	}
	_, err := io.WriteString(out, strings.Join(sections, "\n"))
	return errors.Wrap(err, "write node types")
}

// markdown renders one node type as a section that ends with a newline.
func markdown(nodeType NodeType) string {
	var section strings.Builder
	fmt.Fprintf(&section, "## %s\n\n%s\n", nodeType.Name, nodeType.Description)
	table := nodeType.Table
	if table == "" {
		return section.String()
	}
	if len(nodeType.Settings) == 0 {
		fmt.Fprintf(&section, "\nWrite it as the empty table `[%s]`.\n", table)
		return section.String()
	}
	fmt.Fprintf(&section, "\nWrite it as the table `[%s]`, with these settings:\n\n", table)
	for _, setting := range nodeType.Settings {
		requirement := "optional"
		if setting.Required {
			requirement = "required"
		}
		fmt.Fprintf(&section, "- `%s` (%s): %s\n", setting.Name, requirement, setting.Description)
	}
	return section.String()
}

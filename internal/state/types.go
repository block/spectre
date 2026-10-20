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
	"github.com/block/spectre/internal/terminal"
)

// Library is the node type of shared code that performs no IO.
const Library = "library"

const libraryDescription = "Module shared by more than one other node that performs no IO, " +
	"such as shared domain types, validation, calculations, and helpers."

// TypesConfig selects how node types are printed.
type TypesConfig struct {
	// JSON prints the node types as a JSON array instead of text.
	JSON bool `help:"Print JSON instead of text."`
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
func WriteTypes(out io.Writer, config TypesConfig, palette terminal.Palette, catalog *boundary.Catalog) error {
	types := NodeTypes(catalog)
	if config.JSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return errors.Wrap(encoder.Encode(types), "write node types")
	}
	sections := make([]string, 0, len(types))
	for _, nodeType := range types {
		sections = append(sections, text(palette, nodeType))
	}
	_, err := io.WriteString(out, strings.Join(sections, "\n"))
	return errors.Wrap(err, "write node types")
}

// textWidth is the column that text output wraps at.
const textWidth = 80

// text renders one node type with its description and settings indented below it.
func text(palette terminal.Palette, nodeType NodeType) string {
	var section strings.Builder
	section.WriteString(palette.Bold(nodeType.Name))
	if nodeType.Table != "" {
		section.WriteString(" " + palette.Dim("["+nodeType.Table+"]"))
	}
	section.WriteString("\n" + terminal.Wrap(nodeType.Description, "  ", textWidth))
	if nodeType.Table == "" {
		return section.String()
	}
	if len(nodeType.Settings) == 0 {
		section.WriteString("\n  " + palette.Dim("No settings.") + "\n")
		return section.String()
	}
	writeSettings(&section, palette, nodeType.Settings, "  ")
	return section.String()
}

// writeSettings renders each setting at indent, with the entries of a keyed
// table nested one level deeper.
func writeSettings(section *strings.Builder, palette terminal.Palette, settings []boundary.Setting, indent string) {
	for _, setting := range settings {
		requirement := palette.Dim("(optional)")
		if setting.Required {
			requirement = palette.Yellow("(required)")
		}
		section.WriteString("\n" + indent + palette.Green(setting.Name) + " " + requirement + "\n")
		if setting.Key == "" {
			section.WriteString(terminal.Wrap(setting.Description, indent+"  ", textWidth))
			continue
		}
		section.WriteString(terminal.Wrap(setting.Description+" A table keyed by "+setting.Key+".", indent+"  ", textWidth))
		if len(setting.Entry) == 0 {
			section.WriteString(terminal.Wrap("Each entry takes no settings yet, so write it as {}.", indent+"  ", textWidth))
			continue
		}
		section.WriteString(terminal.Wrap("Each entry is a table with these settings:", indent+"  ", textWidth))
		writeSettings(section, palette, setting.Entry, indent+"    ")
	}
}

package scaffold

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/terminal"
)

// DescribeConfig selects the templates to describe.
type DescribeConfig struct {
	// Name is the template to describe. Every template is listed when empty.
	Name string `arg:"" optional:"" help:"Template to describe with its variables. Lists every template when omitted."`
	// JSON prints the description as JSON.
	JSON bool `help:"Print the templates and their variables as JSON."`
}

// Description is a template with the variables of every template it extends.
type Description struct {
	// Name is the template's registered name.
	Name string `json:"name"`
	// Description says what the template sets up.
	Description string `json:"description"`
	// Extends names the template this one is rendered on top of.
	Extends string `json:"extends,omitzero"`
	// Variables are the values the template asks for, in order.
	Variables []Variable `json:"variables"`
}

// Describe writes the named template, or every template, to out.
func Describe(out io.Writer, config DescribeConfig, registry *Registry, palette terminal.Palette) error {
	names := registry.Names()
	if config.Name != "" {
		names = []string{config.Name}
	}
	descriptions := make([]Description, 0, len(names))
	for _, name := range names {
		template, err := registry.Load(name)
		if err != nil {
			return err
		}
		descriptions = append(descriptions, Description{
			Name:        name,
			Description: template.Manifest.Description,
			Extends:     template.Manifest.Extends,
			Variables:   template.Variables,
		})
	}
	if config.JSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		if config.Name != "" {
			return errors.Wrap(encoder.Encode(descriptions[0]), "write template")
		}
		return errors.Wrap(encoder.Encode(descriptions), "write templates")
	}
	var text strings.Builder
	for i, description := range descriptions {
		if i > 0 {
			text.WriteString("\n")
		}
		heading := palette.Bold(description.Name)
		if description.Extends != "" {
			heading += palette.Dim(" extends " + description.Extends)
		}
		text.WriteString(heading + "\n")
		text.WriteString(terminal.Wrap(description.Description, "  ", 80))
		if config.Name == "" {
			continue
		}
		for _, variable := range description.Variables {
			text.WriteString("\n  " + palette.Cyan(variable.Name) + palette.Dim(" ("+string(variable.Type)+")") + "\n")
			text.WriteString(terminal.Wrap(variable.Help, "    ", 80))
			if fallback, ok := variable.Default.Get(); ok {
				if fallback == "" {
					fallback = "(empty)"
				}
				fmt.Fprintf(&text, "    default: %s\n", fallback)
			}
			if len(variable.Choices) > 0 {
				fmt.Fprintf(&text, "    one of: %s\n", strings.Join(variable.Choices, ", "))
			}
			if variable.Pattern != "" {
				fmt.Fprintf(&text, "    matches: %s\n", variable.Pattern)
			}
		}
	}
	_, err := io.WriteString(out, text.String())
	return errors.Wrap(err, "write templates")
}

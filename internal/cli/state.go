package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/cli/terminal"
	"github.com/block/spectre/internal/state"
)

// stateCommand groups the commands that read a service's migration state.
type stateCommand struct { //nolint:singleusestruct // A Kong command group; folding it would flatten its subcommands.
	Types    stateTypesCommand    `cmd:"" help:"Describe each node type the migration state can use."`
	Validate stateValidateCommand `cmd:"" help:"Check the migration state for errors, and warn about nodes ingress never reaches."`
}

// stateTypesCommand describes the node types for an agent that writes the migration state.
type stateTypesCommand struct {
	JSON bool `help:"Print JSON instead of text."`
}

func (c *stateTypesCommand) Run(out Output, types []state.NodeType) error {
	if c.JSON {
		encoder := json.NewEncoder(out.Writer)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return errors.Wrap(encoder.Encode(types), "write node types")
	}
	sections := make([]string, 0, len(types))
	for _, nodeType := range types {
		sections = append(sections, nodeTypeText(out.Palette, nodeType))
	}
	_, err := io.WriteString(out.Writer, strings.Join(sections, "\n"))
	return errors.Wrap(err, "write node types")
}

// stateValidateCommand checks a migration state file. It fails when the state
// is invalid; warnings alone do not fail it.
type stateValidateCommand struct {
	State state.Config `embed:""`
}

func (c *stateValidateCommand) Run(out Output, kinds state.Kinds) error {
	path := c.State.StateFile
	loaded, err := loadValid(out, path, kinds)
	if err != nil {
		return err
	}
	for _, warning := range loaded.Unreached() {
		writeProblem(out, path, out.Palette.Yellow("warning"), warning)
	}
	migrated := 0
	for _, node := range loaded.All {
		if node.Status == state.Migrated {
			migrated++
		}
	}
	_, err = fmt.Fprintf(out.Writer, "%s is valid: %d nodes, %d migrated\n", path, len(loaded.All), migrated)
	return errors.Wrap(err, "write validation result")
}

// nextCommand lists the next migration step for the agent doing the migration.
type nextCommand struct {
	State state.Config `embed:""`
	JSON  bool         `help:"Print JSON instead of text."`
}

func (c *nextCommand) Run(out Output, kinds state.Kinds) error {
	path := c.State.StateFile
	loaded, err := loadValid(out, path, kinds)
	if err != nil {
		return err
	}
	step := loaded.Next()
	if pending := loaded.Pending(); len(step) == 0 && len(pending) > 0 {
		ids := make([]string, len(pending))
		for i, node := range pending {
			ids[i] = node.ID
		}
		return errors.Errorf("no ingress node reaches the pending nodes, so SPECTRE cannot verify them: %s", strings.Join(ids, ", "))
	}
	if c.JSON {
		encoder := json.NewEncoder(out.Writer)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return errors.Wrap(encoder.Encode(append([]state.Node{}, step...)), "write next nodes")
	}
	if len(step) == 0 {
		_, err = fmt.Fprintln(out.Writer, "Every node is migrated.")
		return errors.Wrap(err, "write next nodes")
	}
	for _, node := range step {
		if _, err := fmt.Fprintf(out.Writer, "%s %s\n", out.Palette.Bold(node.ID), out.Palette.Dim(strings.Join(node.Types(), ", "))); err != nil {
			return errors.Wrap(err, "write next nodes")
		}
	}
	return nil
}

// loadValid loads the state at path, writing each problem when it is invalid.
func loadValid(out Output, path string, kinds state.Kinds) (state.State, error) {
	loaded, err := kinds.Load(path)
	if invalid, ok := errors.AsType[*state.InvalidError](err); ok {
		for _, problem := range invalid.Problems {
			writeProblem(out, path, out.Palette.Red("error"), problem)
		}
		return state.State{}, errors.Errorf("%s is invalid", path)
	}
	return loaded, errors.WithStack(err)
}

// doneCommand marks nodes migrated after the agent adds them to the candidate.
// It writes nothing unless the state stays valid.
type doneCommand struct {
	State state.Config `embed:""`
	Nodes []string     `arg:"" name:"node" help:"IDs of the nodes now in the candidate."`
}

func (c *doneCommand) Run(out Output, kinds state.Kinds) error {
	path := c.State.StateFile
	info, err := os.Stat(path)
	if err != nil {
		return errors.Wrap(err, "read migration state")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.Wrap(err, "read migration state")
	}
	updated, marked, err := kinds.MarkMigrated(data, c.Nodes)
	if invalid, ok := errors.AsType[*state.InvalidError](err); ok {
		for _, problem := range invalid.Problems {
			writeProblem(out, path, out.Palette.Red("error"), problem)
		}
		return errors.Errorf("%s would be invalid, so it is unchanged", path)
	}
	if err != nil {
		return errors.WithStack(err)
	}
	if len(marked) == 0 {
		_, err = fmt.Fprintf(out.Writer, "%s is unchanged: every node is already migrated\n", path)
		return errors.Wrap(err, "write done result")
	}
	if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
		return errors.Wrap(err, "write migration state")
	}
	_, err = fmt.Fprintf(out.Writer, "%s: marked %s migrated\n", path, strings.Join(marked, ", "))
	return errors.Wrap(err, "write done result")
}

// textWidth is the column that text output wraps at.
const textWidth = 80

// nodeTypeText renders one node type with its description and settings indented below it.
func nodeTypeText(palette terminal.Palette, nodeType state.NodeType) string {
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
func writeSettings(section *strings.Builder, palette terminal.Palette, settings []state.Setting, indent string) {
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

// writeProblem writes a problem as path:line: severity: message.
func writeProblem(out Output, path, severity string, problem state.Problem) {
	location := path
	if problem.Line != 0 {
		location = fmt.Sprintf("%s:%d", path, problem.Line)
	}
	_, _ = fmt.Fprintf(out.Writer, "%s: %s: %s\n", location, severity, problem.Message)
}

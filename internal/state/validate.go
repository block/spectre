package state

import (
	"fmt"
	"io"
	"os"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/terminal"
)

// Config locates the migration state file.
type Config struct {
	// StateFile is the path to the migration state TOML file.
	StateFile string `default:"spectre/state.toml" type:"path" help:"Path to the migration state TOML file."`
}

// Load reads and parses the migration state file at path.
func Load(path string, catalog *boundary.Catalog) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, errors.Wrap(err, "read migration state")
	}
	return errors.WithStack2(Parse(data, catalog))
}

// Validate checks the state file and writes each error and warning to out. It
// fails when the state is invalid; warnings alone do not fail it.
func Validate(out io.Writer, config Config, palette terminal.Palette, catalog *boundary.Catalog) error {
	state, err := Load(config.StateFile, catalog)
	if invalid, ok := errors.AsType[*InvalidError](err); ok {
		for _, problem := range invalid.Problems {
			writeProblem(out, config.StateFile, palette.Red("error"), problem)
		}
		return errors.Errorf("%s is invalid", config.StateFile)
	}
	if err != nil {
		return err
	}
	for _, warning := range state.Unreached() {
		writeProblem(out, config.StateFile, palette.Yellow("warning"), warning)
	}
	migrated := 0
	for _, node := range state.Nodes {
		if node.Status == Migrated {
			migrated++
		}
	}
	_, err = fmt.Fprintf(out, "%s is valid: %d nodes, %d migrated\n", config.StateFile, len(state.Nodes), migrated)
	return errors.Wrap(err, "write validation result")
}

// writeProblem writes a problem as path:line: severity: message.
func writeProblem(out io.Writer, path, severity string, problem Problem) {
	location := path
	if problem.Line != 0 {
		location = fmt.Sprintf("%s:%d", path, problem.Line)
	}
	_, _ = fmt.Fprintf(out, "%s: %s: %s\n", location, severity, problem.Message)
}

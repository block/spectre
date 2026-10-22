package scaffold

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/pelletier/go-toml"
)

// migrationFile is the checkout's migration config, where init keeps its
// answers in the [init] table.
const migrationFile = "spectre/migration.toml"

const answersComment = "# Written by spectre init, which reads it on the next run. Change it with --var.\n"

// Answers are the template and variable values of a run of init, which the
// next run reuses.
type Answers struct {
	// Template is the name of the rendered template.
	Template string `toml:"template"`
	// Variables are the values the template was rendered with.
	Variables map[string]string `toml:"variables"`
}

// newAnswers records a run's template and values.
func newAnswers(template string, values map[string]string) Answers {
	return Answers{Template: template, Variables: values}
}

// readAnswers reads the [init] table of the migration config in dir, and
// reports whether there is one.
func readAnswers(dir string) (answers Answers, found bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, migrationFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Answers{}, false, nil
	}
	if err != nil {
		return Answers{}, false, errors.Wrap(err, "read migration config")
	}
	var config struct {
		Init Answers `toml:"init"`
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return Answers{}, false, errors.Wrapf(err, "parse %s", migrationFile)
	}
	return config.Init, config.Init.Template != "", nil
}

// writeAnswers replaces the [init] table of the migration config in dir with
// answers, keeping the rest of the file.
func writeAnswers(dir string, answers Answers) error {
	path := filepath.Join(dir, migrationFile)
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.Wrap(err, "read migration config")
	}
	var table bytes.Buffer
	if err := toml.NewEncoder(&table).Indentation("").Encode(struct {
		Init Answers `toml:"init"`
	}{answers}); err != nil {
		return errors.Wrap(err, "encode init answers")
	}
	content := strings.TrimRight(withoutInit(string(existing)), "\n")
	if content != "" {
		content += "\n\n"
	}
	header, rest, _ := strings.Cut(strings.TrimLeft(table.String(), "\n"), "\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // A service checkout.
		return errors.WithStack(err)
	}
	return errors.Wrap(os.WriteFile(path, []byte(content+header+"\n"+answersComment+rest), 0o644), "write migration config")
}

// withoutInit removes the [init] table and its subtables from a TOML document.
func withoutInit(document string) string {
	var kept strings.Builder
	inInit := false
	for line := range strings.Lines(document) {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "[") {
			inInit = trimmed == "[init]" || strings.HasPrefix(trimmed, "[init.")
		}
		if !inInit {
			kept.WriteString(line)
		}
	}
	return kept.String()
}

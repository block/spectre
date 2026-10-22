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

// recordAnswers sets the migration config in files to its rendered content, or
// the checkout's when customisable or not rendered, with answers as [init].
func recordAnswers(dir string, files map[string]File, answers Answers) error {
	file, rendered := files[migrationFile]
	if !rendered {
		file.Mode = 0o644
	}
	if file.Customisable || !rendered {
		existing, err := os.ReadFile(filepath.Join(dir, migrationFile))
		switch {
		case err == nil:
			file.Content = existing
		case !errors.Is(err, fs.ErrNotExist):
			return errors.Wrap(err, "read migration config")
		}
	}
	var table bytes.Buffer
	if err := toml.NewEncoder(&table).Indentation("").Encode(struct {
		Init Answers `toml:"init"`
	}{answers}); err != nil {
		return errors.Wrap(err, "encode init answers")
	}
	content := strings.TrimRight(withoutInit(string(file.Content)), "\n")
	if content != "" {
		content += "\n\n"
	}
	header, rest, _ := strings.Cut(strings.TrimLeft(table.String(), "\n"), "\n")
	files[migrationFile] = File{Content: []byte(content + header + "\n" + answersComment + rest), Mode: file.Mode}
	return nil
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

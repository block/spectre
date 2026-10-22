package scaffold

import (
	"io/fs"
	"os"
	"regexp"

	"github.com/alecthomas/errors"
	"github.com/block/scaffolder"
)

// Template is a fetched template with its manifest.
type Template struct {
	// Name is the template's directory in its repository.
	Name string
	// Manifest is the template's manifest.
	Manifest Manifest

	files fs.FS
}

func newTemplate(name string, manifest Manifest, files fs.FS) Template {
	return Template{Name: name, Manifest: manifest, files: files}
}

// NextSteps renders the template's next steps.
func (t Template) NextSteps(values map[string]any) (string, error) {
	if t.Manifest.NextSteps == "" {
		return "", nil
	}
	return errors.WithStack2(renderText("next steps", t.Manifest.NextSteps, values))
}

// Render renders the template into dir with values, except the slash-separated
// paths in keep.
func (t Template) Render(dir string, values map[string]any, keep []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // A service checkout.
		return errors.WithStack(err)
	}
	exclude := []string{"^" + regexp.QuoteMeta(manifestFile) + "$"}
	for _, name := range keep {
		exclude = append(exclude, "^"+regexp.QuoteMeta(name)+`(\.tmpl)?$`)
	}
	return errors.WithStack(scaffolder.Scaffold(t.files, dir, values,
		scaffolder.Functions(templateFuncs()),
		scaffolder.Exclude(exclude...),
	))
}

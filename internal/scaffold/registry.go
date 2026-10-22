package scaffold

import (
	"embed"
	"io/fs"
	"maps"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
)

// BaseTemplate is the name of the placeholder template the binary ships.
const BaseTemplate = "base"

//go:embed all:templates/base
var baseFiles embed.FS

// Registry holds the templates init can render, by name. A custom binary adds
// to it through spectre.WithTemplate.
type Registry struct {
	templates map[string]fs.FS
}

// NewRegistry returns a registry holding the base template.
func NewRegistry() *Registry {
	base, err := fs.Sub(baseFiles, "templates/base")
	if err != nil {
		panic(err) // The embedded directory always exists.
	}
	return &Registry{templates: map[string]fs.FS{BaseTemplate: base}}
}

// Register adds a template, replacing any earlier one with the same name.
// files holds the template's manifest at its root.
func (r *Registry) Register(name string, files fs.FS) {
	r.templates[name] = files
}

// Names returns the registered template names, sorted.
func (r *Registry) Names() []string {
	return slices.Sorted(maps.Keys(r.templates))
}

// Load reads a template and every template it extends.
func (r *Registry) Load(name string) (template Template, err error) {
	var layers []layer
	seen := map[string]bool{}
	for next := name; next != ""; {
		if seen[next] {
			return template, errors.Errorf("template %q extends itself", name)
		}
		seen[next] = true
		files, ok := r.templates[next]
		if !ok {
			return template, errors.Errorf("unknown template %q; available: %s", next, strings.Join(r.Names(), ", "))
		}
		manifest, err := ParseManifest(files)
		if err != nil {
			return template, errors.Wrapf(err, "template %q", next)
		}
		layers = append([]layer{{Name: next, Files: files, Manifest: manifest}}, layers...)
		next = manifest.Extends
	}
	return newTemplate(name, layers), nil
}

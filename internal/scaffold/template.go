package scaffold

import (
	"bytes"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/block/scaffolder"
)

// Template is a template rendered on top of the templates it extends.
type Template struct {
	// Name is the template's registered name.
	Name string
	// Manifest is the named template's own manifest.
	Manifest Manifest
	// Variables are the variables of every layer, parents first. A child's
	// variable replaces its parent's in place.
	Variables []Variable

	layers []layer
}

// layer is one template in a chain, parents first.
type layer struct {
	Name     string
	Files    fs.FS
	Manifest Manifest
}

// File is a rendered file, or a symbolic link when Link is set.
type File struct {
	// Content is the rendered content of a regular file.
	Content []byte
	// Link is the target of a symbolic link.
	Link string
	// Customisable files are written only when absent and never deleted.
	Customisable bool
	// Executable files are written with execute permission.
	Executable bool
}

func newTemplate(name string, layers []layer) Template {
	var variables []Variable
	index := map[string]int{}
	for _, l := range layers {
		for _, variable := range l.Manifest.Variables {
			if i, ok := index[variable.Name]; ok {
				variables[i] = variable
				continue
			}
			index[variable.Name] = len(variables)
			variables = append(variables, variable)
		}
	}
	return Template{Name: name, Manifest: layers[len(layers)-1].Manifest, Variables: variables, layers: layers}
}

// NextSteps renders the next steps of the closest layer that has them.
func (t Template) NextSteps(values map[string]any) (string, error) {
	for i := len(t.layers) - 1; i >= 0; i-- {
		if steps := t.layers[i].Manifest.NextSteps; steps != "" {
			return errors.WithStack2(renderText("next steps", steps, values))
		}
	}
	return "", nil
}

// Render renders every layer with values and merges them, keyed by
// slash-separated path. A child's files replace and exclude its parent's.
func (t Template) Render(values map[string]any) (map[string]File, error) {
	files := map[string]File{}
	for _, l := range t.layers {
		maps.DeleteFunc(files, func(name string, _ File) bool { return matchAny(l.Manifest.Files.Exclude, name) })
		rendered, err := l.render(values)
		if err != nil {
			return nil, errors.Wrapf(err, "render template %q", l.Name)
		}
		maps.Copy(files, rendered)
	}
	if _, ok := files[lockFile]; ok {
		return nil, errors.Errorf("templates cannot render %s", lockFile)
	}
	return files, nil
}

// render copies the layer to disk for scaffolder, which reads only from disk,
// then collects what it renders.
func (l layer) render(values map[string]any) (map[string]File, error) {
	work, err := os.MkdirTemp("", "spectre-template-*")
	if err != nil {
		return nil, errors.Wrap(err, "create template directory")
	}
	defer os.RemoveAll(work) //nolint:errcheck // Best-effort cleanup of a temporary directory.
	source := filepath.Join(work, "source")
	output := filepath.Join(work, "output")
	if err := os.CopyFS(source, l.Files); err != nil {
		return nil, errors.Wrap(err, "copy template")
	}
	err = scaffolder.Scaffold(source, output, values,
		scaffolder.Functions(templateFuncs()),
		scaffolder.Exclude("^"+regexp.QuoteMeta(manifestFile)+"$"),
	)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	files := map[string]File{}
	err = filepath.WalkDir(output, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return errors.WithStack(err)
		}
		rel, err := filepath.Rel(output, path)
		if err != nil {
			return errors.WithStack(err)
		}
		name := filepath.ToSlash(rel)
		if bytes.Contains(content, []byte("<no value>")) {
			return errors.Errorf("%s refers to an undeclared variable", name)
		}
		files[name] = File{
			Content:      content,
			Customisable: matchAny(l.Manifest.Files.Customisable, name),
			Executable:   matchAny(l.Manifest.Files.Executable, name),
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "collect rendered files")
	}
	for link, target := range l.Manifest.Symlinks {
		name, err := renderText("symlink", link, values)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if target, err = renderText("symlink target", target, values); err != nil {
			return nil, errors.WithStack(err)
		}
		if !filepath.IsLocal(name) || strings.Contains(name, "\\") {
			return nil, errors.Errorf("symlink %q must be a relative path inside the checkout", name)
		}
		files[name] = File{Link: target, Customisable: matchAny(l.Manifest.Files.Customisable, name)}
	}
	return files, nil
}

package scaffold

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/block/scaffolder"
)

// Template is a registered template with its manifest.
type Template struct {
	// Name is the template's registered name.
	Name string
	// Manifest is the template's manifest.
	Manifest Manifest

	files fs.FS
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

// Render renders the template with values, keyed by slash-separated path.
func (t Template) Render(values map[string]any) (map[string]File, error) {
	work, err := os.MkdirTemp("", "spectre-template-*")
	if err != nil {
		return nil, errors.Wrap(err, "create output directory")
	}
	defer os.RemoveAll(work) //nolint:errcheck // Best-effort cleanup of a temporary directory.
	output := filepath.Join(work, "output")
	err = scaffolder.Scaffold(t.files, output, values,
		scaffolder.Functions(templateFuncs()),
		scaffolder.Exclude("^"+regexp.QuoteMeta(manifestFile)+"$"),
		scaffolder.AfterEach(readable),
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
			Customisable: matchAny(t.Manifest.Files.Customisable, name),
			Executable:   matchAny(t.Manifest.Files.Executable, name),
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "collect rendered files")
	}
	for link, target := range t.Manifest.Symlinks {
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
		files[name] = File{Link: target, Customisable: matchAny(t.Manifest.Files.Customisable, name)}
	}
	return files, nil
}

// readable lets Render read back a rendered file whatever mode the template gives it.
func readable(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.WithStack(err)
	}
	return errors.WithStack(os.Chmod(path, 0o600))
}

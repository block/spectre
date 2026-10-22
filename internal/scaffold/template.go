package scaffold

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
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

// File is a rendered file, or a symbolic link when Link is set.
type File struct {
	// Content is the rendered content of a regular file.
	Content []byte
	// Link is the target of a symbolic link.
	Link string
	// Mode is the permission bits of a regular file, as in the template.
	Mode fs.FileMode
	// Customisable files are written only when absent and never deleted.
	Customisable bool
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
	)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	files := map[string]File{}
	err = filepath.WalkDir(output, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(output, path)
		if err != nil {
			return errors.WithStack(err)
		}
		name := filepath.ToSlash(rel)
		file, err := readRendered(path, entry)
		if err != nil {
			return errors.Wrap(err, name)
		}
		file.Customisable = matchAny(t.Manifest.Files.Customisable, name)
		files[name] = file
		return nil
	})
	return files, errors.Wrap(err, "collect rendered files")
}

// readRendered reads a file or symbolic link scaffolder rendered at path.
func readRendered(path string, entry fs.DirEntry) (File, error) {
	if entry.Type()&fs.ModeSymlink != 0 {
		link, err := os.Readlink(path)
		return File{Link: link}, errors.WithStack(err)
	}
	info, err := entry.Info()
	if err != nil {
		return File{}, errors.WithStack(err)
	}
	if !info.Mode().IsRegular() {
		return File{}, errors.Errorf("unsupported file type %s", info.Mode().Type())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return File{}, errors.WithStack(err)
	}
	if bytes.Contains(content, []byte("<no value>")) {
		return File{}, errors.New("refers to an undeclared variable")
	}
	return File{Content: content, Mode: info.Mode().Perm()}, nil
}

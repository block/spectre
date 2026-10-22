package scaffold

import (
	"bytes"
	"io/fs"
	"path"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/pelletier/go-toml"
)

// manifestFile is the template's manifest at the root of its file tree. It is
// never rendered.
const manifestFile = "scaffold.toml"

// Manifest describes a template: the values it asks for, and which of its
// files are customisable.
type Manifest struct {
	// Variables are the values the template asks for, in order.
	Variables []Variable `toml:"variables"`
	// Files sets how the template's rendered files are written.
	Files Files `toml:"files"`
	// NextSteps is a template printed after the files are written.
	NextSteps string `toml:"next_steps"`
}

// Files holds path patterns over a template's rendered paths. A pattern is a
// path.Match pattern, and a trailing /** matches everything below a directory.
type Files struct {
	// Customisable files are written only when absent and never deleted. They
	// are in spectre/, with names that are not templates.
	Customisable []string `toml:"customisable"`
}

// ParseManifest reads the manifest at the root of files.
func ParseManifest(files fs.FS) (Manifest, error) {
	data, err := fs.ReadFile(files, manifestFile)
	if err != nil {
		return Manifest{}, errors.Wrap(err, "read template manifest")
	}
	var manifest Manifest
	if err := toml.NewDecoder(bytes.NewReader(data)).Strict(true).Decode(&manifest); err != nil {
		return Manifest{}, errors.Wrap(err, "parse template manifest")
	}
	seen := map[string]bool{}
	for i, variable := range manifest.Variables {
		if seen[variable.Name] {
			return Manifest{}, errors.Errorf("variable %q is declared twice", variable.Name)
		}
		seen[variable.Name] = true
		normalised, err := variable.normalise()
		if err != nil {
			return Manifest{}, errors.WithStack(err)
		}
		manifest.Variables[i] = normalised
	}
	for _, pattern := range manifest.Files.Customisable {
		if _, err := path.Match(strings.TrimSuffix(pattern, "/**"), ""); err != nil {
			return Manifest{}, errors.Errorf("invalid file pattern %q", pattern)
		}
		if !strings.HasPrefix(pattern, managedDir+"/") {
			return Manifest{}, errors.Errorf("customisable pattern %q must be in %s/", pattern, managedDir)
		}
	}
	err = fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if strings.Contains(name, "{{") && matchAny(manifest.Files.Customisable, strings.TrimSuffix(name, ".tmpl")) {
			return errors.Errorf("customisable file %q must not have a template as its name", name)
		}
		return nil
	})
	if err != nil {
		return Manifest{}, errors.WithStack(err)
	}
	return manifest, nil
}

// matchAny reports whether a slash-separated path matches any pattern.
func matchAny(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if dir, ok := strings.CutSuffix(pattern, "/**"); ok {
			for parent := name; parent != "."; parent = path.Dir(parent) {
				if globMatch(dir, parent) {
					return true
				}
			}
			continue
		}
		if globMatch(pattern, name) {
			return true
		}
	}
	return false
}

// globMatch reports whether name matches pattern. ParseManifest has checked the pattern.
func globMatch(pattern, name string) bool {
	matched, err := path.Match(pattern, name)
	return err == nil && matched
}

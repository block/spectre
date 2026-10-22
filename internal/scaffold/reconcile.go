package scaffold

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/alecthomas/errors"
)

// managedDir is the checkout directory init owns. Files there that are neither
// rendered nor customisable are deleted.
const managedDir = "spectre"

// apply writes the rendered files into dir, except customisable ones already
// there, then deletes every other file in managedDir that is not customisable.
func apply(dir string, files map[string]File, customisable []string) error {
	for _, name := range slices.Sorted(maps.Keys(files)) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if _, err := os.Lstat(path); err == nil && files[name].Customisable {
			continue
		}
		if err := write(path, files[name]); err != nil {
			return errors.Wrap(err, name)
		}
	}
	var stale []string
	err := filepath.WalkDir(filepath.Join(dir, managedDir), func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) || (err == nil && entry.IsDir()) {
			return nil
		}
		if err != nil {
			return errors.WithStack(err)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return errors.WithStack(err)
		}
		name := filepath.ToSlash(rel)
		if _, rendered := files[name]; !rendered && !matchAny(customisable, name) {
			stale = append(stale, path)
		}
		return nil
	})
	if err != nil {
		return errors.Wrapf(err, "read %s", managedDir)
	}
	for _, path := range stale {
		if err := remove(dir, path); err != nil {
			return errors.Wrapf(err, "delete %s", path)
		}
	}
	return nil
}

// write replaces whatever is at path, without following a symbolic link there.
func write(path string, file File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // Checked in.
		return errors.WithStack(err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.WithStack(err)
	}
	if file.Link != "" {
		return errors.WithStack(os.Symlink(file.Link, path))
	}
	mode := fs.FileMode(0o644)
	if file.Executable {
		mode = 0o755
	}
	return errors.WithStack(os.WriteFile(path, file.Content, mode))
}

// remove deletes path, then each parent directory up to dir left empty.
func remove(dir, path string) error {
	if err := os.Remove(path); err != nil {
		return errors.WithStack(err)
	}
	for parent := filepath.Dir(path); parent != dir && len(parent) > len(dir); parent = filepath.Dir(parent) {
		entries, err := os.ReadDir(parent)
		if err != nil {
			return errors.WithStack(err)
		}
		if len(entries) > 0 {
			break
		}
		if err := os.Remove(parent); err != nil {
			return errors.WithStack(err)
		}
	}
	return nil
}

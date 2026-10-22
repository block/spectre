package scaffold

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/alecthomas/errors"
)

// managedDir is the checkout directory init owns. Each run replaces the files
// there, except customisable ones.
const managedDir = "spectre"

// clean deletes the files in managedDir below dir, except customisable ones
// and the migration config, and returns the customisable ones it kept.
func clean(dir string, customisable []string) (kept []string, err error) {
	var stale []string
	err = filepath.WalkDir(filepath.Join(dir, managedDir), func(path string, entry fs.DirEntry, err error) error {
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
		switch name := filepath.ToSlash(rel); {
		case matchAny(customisable, name):
			kept = append(kept, name)
		case name != migrationFile:
			stale = append(stale, path)
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrapf(err, "read %s", managedDir)
	}
	for _, path := range stale {
		if err := remove(dir, path); err != nil {
			return nil, errors.Wrapf(err, "delete %s", path)
		}
	}
	return kept, nil
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

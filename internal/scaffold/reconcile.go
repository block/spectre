package scaffold

import (
	"bytes"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/alecthomas/errors"
)

// Action is what init does to one file.
type Action string

// The actions. Keep leaves a customisable file that differs from the template
// as it is.
const (
	Create    Action = "create"
	Update    Action = "update"
	Delete    Action = "delete"
	Keep      Action = "keep"
	Unchanged Action = "unchanged"
)

// Change is the action init takes on one file.
type Change struct {
	// Path is the file's slash-separated path in the checkout.
	Path string
	// Action is what init does to it.
	Action Action
}

// managedDir is the checkout directory init owns. Files there that are neither
// rendered nor customisable are deleted.
const managedDir = "spectre"

// plan decides the action for each rendered file, and for each file in
// managedDir that is no longer rendered.
func plan(dir string, files map[string]File, customisable []string) ([]Change, error) {
	var changes []Change
	for _, name := range slices.Sorted(maps.Keys(files)) {
		action, err := planFile(filepath.Join(dir, filepath.FromSlash(name)), files[name])
		if err != nil {
			return nil, errors.Wrap(err, name)
		}
		changes = append(changes, Change{Path: name, Action: action})
	}
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
			changes = append(changes, Change{Path: name, Action: Delete})
		}
		return nil
	})
	return changes, errors.Wrapf(err, "read %s", managedDir)
}

func planFile(path string, file File) (Action, error) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Create, nil
	case err != nil:
		return "", errors.WithStack(err)
	case info.IsDir():
		return "", errors.New("a directory is in the way")
	}
	same, err := matches(path, info, file)
	switch {
	case err != nil:
		return "", err
	case same:
		return Unchanged, nil
	case file.Customisable:
		return Keep, nil
	default:
		return Update, nil
	}
}

// matches reports whether what is at path is the rendered file.
func matches(path string, info fs.FileInfo, file File) (same bool, err error) {
	if file.Link != "" {
		if info.Mode()&fs.ModeSymlink == 0 {
			return false, nil
		}
		target, err := os.Readlink(path)
		return target == file.Link, errors.WithStack(err)
	}
	if !info.Mode().IsRegular() || (info.Mode()&0o111 != 0) != file.Executable {
		return false, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false, errors.WithStack(err)
	}
	return bytes.Equal(content, file.Content), nil
}

// apply carries out the changes in dir.
func apply(dir string, files map[string]File, changes []Change) error {
	for _, change := range changes {
		path := filepath.Join(dir, filepath.FromSlash(change.Path))
		var err error
		switch change.Action {
		case Create, Update:
			err = write(path, files[change.Path])
		case Delete:
			err = remove(dir, path)
		case Keep, Unchanged:
		}
		if err != nil {
			return errors.Wrap(err, change.Path)
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

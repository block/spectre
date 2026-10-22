package scaffold

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/alecthomas/errors"
	"github.com/pelletier/go-toml"
)

// lockFile records, relative to the checkout, what the last run of init wrote.
const lockFile = "spectre/init.lock.toml"

const lockHeader = "# Written by spectre init, which reads it on the next run. Do not edit.\n"

// Lock is what a run of init wrote, so the next run can reuse its answers and
// delete files the template no longer renders.
type Lock struct {
	// Template is the name of the rendered template.
	Template string `toml:"template"`
	// Managed are the files a run overwrites, and deletes once not rendered.
	Managed []string `toml:"managed"`
	// Customisable are the files written only when absent.
	Customisable []string `toml:"customisable"`
	// Variables are the values the template was rendered with, as written on
	// the command line.
	Variables map[string]string `toml:"variables"`
}

// newLock records the files a run rendered.
func newLock(template string, values map[string]any, files map[string]File) Lock {
	lock := Lock{Template: template, Managed: []string{}, Customisable: []string{}, Variables: map[string]string{}}
	for name, value := range values {
		lock.Variables[name] = format(value)
	}
	for name, file := range files {
		if file.Customisable {
			lock.Customisable = append(lock.Customisable, name)
		} else {
			lock.Managed = append(lock.Managed, name)
		}
	}
	slices.Sort(lock.Managed)
	slices.Sort(lock.Customisable)
	return lock
}

// readLock reads the lock file in dir, and reports whether there is one.
func readLock(dir string) (lock Lock, found bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, lockFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Lock{}, false, nil
	}
	if err != nil {
		return Lock{}, false, errors.Wrap(err, "read init lock")
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).Strict(true).Decode(&lock); err != nil {
		return Lock{}, false, errors.Wrapf(err, "parse %s", lockFile)
	}
	return lock, true, nil
}

// write writes the lock file into dir.
func (l Lock) write(dir string) error {
	var data bytes.Buffer
	if err := toml.NewEncoder(&data).Indentation("").Order(toml.OrderPreserve).Encode(l); err != nil {
		return errors.Wrap(err, "encode init lock")
	}
	path := filepath.Join(dir, lockFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return errors.Wrap(err, "create init lock directory")
	}
	return errors.Wrap(os.WriteFile(path, append([]byte(lockHeader), data.Bytes()...), 0o644), "write init lock") //nolint:gosec // Checked in.
}

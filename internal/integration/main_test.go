//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/alecthomas/errors"
)

// TestMain builds the sample's descriptors once, then runs the tests.
func TestMain(m *testing.M) {
	if err := build("sample-descriptors"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// build runs bit in the repository root for targets.
func build(targets ...string) error {
	root := repositoryRoot()
	command := exec.CommandContext(context.Background(), filepath.Join(root, "bin", "bit"), append([]string{"-q"}, targets...)...)
	command.Dir = root
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	return errors.Wrap(command.Run(), "build the test's dependencies")
}

// repositoryRoot finds the root from this file's path, two directories down.
func repositoryRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

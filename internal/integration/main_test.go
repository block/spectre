//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/integration/it"
)

// TestMain builds the sample's descriptors once, runs the tests, then removes the
// MySQL server they shared.
func TestMain(m *testing.M) {
	if err := build("sample-descriptors"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	it.RemoveMySQLServer()
	os.Exit(code)
}

// build runs bit in the repository root for targets.
func build(targets ...string) error {
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	command := exec.CommandContext(context.Background(), filepath.Join(root, "bin", "bit"), append([]string{"-q"}, targets...)...)
	command.Dir = root
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	return errors.Wrap(command.Run(), "build the test's dependencies")
}

// repositoryRoot asks git, because -trimpath hides this file's path.
func repositoryRoot() (root string, err error) {
	output, err := exec.CommandContext(context.Background(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", errors.Wrap(err, "find the repository root")
	}
	return strings.TrimSpace(string(output)), nil
}

package integration_test

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/alecthomas/errors"
)

// root is the repository root, which holds Procfile.integration and bin/proctor.
var root string

func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Short() {
		if err := setUp(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

// setUp finds the repository root and builds what every mirror runs once, so
// parallel tests do not all build it as they start.
func setUp() error {
	var err error
	root, err = filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return errors.Wrap(err, "find repository root")
	}
	build := exec.Command(filepath.Join(root, "bin", "bit"), "-q", "spectre-bin", "sample-bin", "sample-forecasts-bin", "sample-descriptors")
	build.Dir = root
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return errors.Wrap(err, "build the mirror's binaries and descriptors")
	}
	return nil
}

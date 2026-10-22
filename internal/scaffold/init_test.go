package scaffold_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/scaffold"
)

func TestInitErrors(t *testing.T) {
	setUp := t.TempDir()
	assert.NoError(t, os.Mkdir(filepath.Join(setUp, "spectre"), 0o755))
	tests := []struct {
		name   string
		config scaffold.Config
		error  string
	}{
		{name: "NoRepo", config: scaffold.Config{Dir: t.TempDir(), Template: "app"}, error: "no template repository; pass --template-repo, or build spectre with spectre.WithTemplateRepo"},
		{name: "AlreadySetUp", config: scaffold.Config{Dir: setUp, Template: "app", Repo: t.TempDir()}, error: setUp + " already has a spectre directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.EqualError(t, scaffold.Init(t.Context(), test.config), test.error)
		})
	}
}

package scaffold_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

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
		{name: "AlreadySetUp", config: scaffold.Config{Dir: setUp, Template: "app", Repo: t.TempDir()}, error: filepath.Join(setUp, "spectre") + " already exists"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.EqualError(t, scaffold.Init(t.Context(), test.config), test.error)
		})
	}
}

func TestInitStopsWhenTemplateScriptThrows(t *testing.T) {
	repo := t.TempDir()
	assert.NoError(t, os.CopyFS(filepath.Join(repo, "app"), fstest.MapFS{
		"template.js": {Data: []byte(`if (context.name === undefined) { throw new Error("pass --var name=NAME"); }`)},
		"file.txt":    {Data: []byte("{{ .name }}")},
	}))
	dir := t.TempDir()
	err := scaffold.Init(t.Context(), scaffold.Config{Dir: dir, Template: "app", Repo: repo})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pass --var name=NAME")
	_, err = os.Stat(filepath.Join(dir, "spectre"))
	assert.True(t, os.IsNotExist(err))

	assert.NoError(t, scaffold.Init(t.Context(), scaffold.Config{Dir: dir, Template: "app", Repo: repo, Vars: map[string]string{"name": "svc"}}))
	entries, err := os.ReadDir(filepath.Join(dir, "spectre"))
	assert.NoError(t, err)
	assert.Equal(t, 1, len(entries))
	assert.Equal(t, "file.txt", entries[0].Name())
}

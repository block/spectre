package scaffold_test

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/scaffold"
)

const appManifest = `
next_steps = "Edit {{ .name }}."

[[variables]]
name = "name"
help = "Service name."

[[variables]]
name = "envs"
help = "Environments."

[files]
customisable = ["config.toml", "spectre/custom/**", "spectre/migration.toml"]
`

func appTemplate() fstest.MapFS {
	return fstest.MapFS{
		"scaffold.toml":                  {Data: []byte(appManifest)},
		"config.toml.tmpl":               {Data: []byte("name = \"{{ .name }}\"\nenvs = [{{ range $i, $env := split .envs }}{{ if $i }}, {{ end }}\"{{ $env }}\"{{ end }}]\n")},
		"spectre/bin/run":                {Data: []byte("#!/bin/sh\necho {{ .name }}\n"), Mode: 0o755},
		"spectre/link/{{ .name }}":       {Data: []byte("../bin/run"), Mode: fs.ModeSymlink},
		"spectre/custom/{{ .name }}.txt": {Data: []byte("{{ .name }}\n")},
		"spectre/migration.toml":         {Data: []byte("app = \"{{ .name }}\"\n")},
		"spectre/placeholder.txt":        {Data: []byte("placeholder\n")},
		"README.md":                      {Data: []byte("# {{ .name }}\n")},
	}
}

// nextTemplate is a later app template that drops spectre/placeholder.txt and manages config.toml.
func nextTemplate() fstest.MapFS {
	files := appTemplate()
	delete(files, "spectre/placeholder.txt")
	delete(files, "config.toml.tmpl")
	manifest := strings.Replace(appManifest, `customisable = ["config.toml", "spectre/custom/**", "spectre/migration.toml"]`, `customisable = ["spectre/custom/**", "spectre/migration.toml"]`, 1)
	manifest = strings.Replace(manifest, "[files]", `[[variables]]
name = "image"
help = "Image."

[files]`, 1)
	files["scaffold.toml"] = &fstest.MapFile{Data: []byte(manifest)}
	files["config.toml"] = &fstest.MapFile{Data: []byte("image = \"{{ .image }}\"\n")}
	files["next.txt"] = &fstest.MapFile{Data: []byte("{{ .image }} {{ .envs }}\n")}
	return files
}

// newRepo writes templates into a local template repository.
func newRepo(t *testing.T, templates map[string]fstest.MapFS) string {
	t.Helper()
	repo := t.TempDir()
	for name, files := range templates {
		assert.NoError(t, os.CopyFS(filepath.Join(repo, name), files))
	}
	return repo
}

func appRepo(t *testing.T) string {
	t.Helper()
	return newRepo(t, map[string]fstest.MapFS{"app": appTemplate(), "next": nextTemplate()})
}

func TestInitWritesTemplate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "svc")
	output := runInit(t, scaffold.Config{Dir: dir, Template: "app", Vars: map[string]string{"name": "svc", "envs": "staging"}}, appRepo(t))

	assert.Equal(t, map[string]string{
		"README.md":               "# svc\n",
		"config.toml":             "name = \"svc\"\nenvs = [\"staging\"]\n",
		"spectre/bin/run":         "exec:#!/bin/sh\necho svc\n",
		"spectre/custom/svc.txt":  "svc\n",
		"spectre/migration.toml":  "app = \"svc\"\n\n" + answers("app", `envs = "staging"`, `name = "svc"`),
		"spectre/link/svc":        "link:../bin/run",
		"spectre/placeholder.txt": "placeholder\n",
	}, readTree(t, dir))
	assert.Equal(t, "Next steps:\nEdit svc.\n", output)
}

func TestInitRerunReconciles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "svc")
	repo := appRepo(t)
	runInit(t, scaffold.Config{Dir: dir, Template: "app", Vars: map[string]string{"name": "first", "envs": "staging"}}, repo)
	writeFile(t, dir, "config.toml", "edited\n")
	writeFile(t, dir, "README.md", "edited\n")
	writeFile(t, dir, "own.txt", "the service's own file\n")
	writeFile(t, dir, "spectre/own.txt", "not in the template\n")
	writeFile(t, dir, "spectre/custom/own.txt", "customised\n")

	runInit(t, scaffold.Config{Dir: dir, Template: "next", Vars: map[string]string{"image": "first-image"}}, repo)

	assert.Equal(t, map[string]string{
		"README.md":                "# first\n",
		"config.toml":              "image = \"first-image\"\n",
		"next.txt":                 "first-image staging\n",
		"own.txt":                  "the service's own file\n",
		"spectre/bin/run":          "exec:#!/bin/sh\necho first\n",
		"spectre/custom/first.txt": "first\n",
		"spectre/custom/own.txt":   "customised\n",
		"spectre/link/first":       "link:../bin/run",
		"spectre/migration.toml":   "app = \"first\"\n\n" + answers("next", `envs = "staging"`, `image = "first-image"`, `name = "first"`),
	}, readTree(t, dir))
}

func TestInitRerunKeepsCustomisableAndDeletesStale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "svc")
	repo := appRepo(t)
	runInit(t, scaffold.Config{Dir: dir, Template: "app", Vars: map[string]string{"name": "first", "envs": "staging"}}, repo)
	writeFile(t, dir, "config.toml", "edited\n")
	writeFile(t, dir, "spectre/migration.toml", strings.Replace(readFile(t, dir, "spectre/migration.toml"), "first", "edited", 1))

	runInit(t, scaffold.Config{Dir: dir, Vars: map[string]string{"name": "second"}}, repo)

	assert.Equal(t, map[string]string{
		"README.md":                 "# second\n",
		"config.toml":               "edited\n",
		"spectre/bin/run":           "exec:#!/bin/sh\necho second\n",
		"spectre/custom/first.txt":  "first\n",
		"spectre/custom/second.txt": "second\n",
		"spectre/link/second":       "link:../bin/run",
		"spectre/migration.toml":    "app = \"edited\"\n\n" + answers("app", `envs = "staging"`, `name = "second"`),
		"spectre/placeholder.txt":   "placeholder\n",
	}, readTree(t, dir))
}

func TestInitVariables(t *testing.T) {
	tests := []struct {
		name   string
		config scaffold.Config
		want   string
		error  string
	}{
		{name: "Flags", config: scaffold.Config{Vars: map[string]string{"name": "flag", "envs": "staging, production"}}, want: "name = \"flag\"\nenvs = [\"staging\", \"production\"]\n"},
		{name: "UnknownVariable", config: scaffold.Config{Vars: map[string]string{"nope": "x"}}, error: `template "app" has no variable "nope"; it has: name, envs`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "svc")
			config := test.config
			config.Dir = dir
			config.Template = "app"
			config.Repo = appRepo(t)
			err := scaffold.Init(t.Context(), io.Discard, config)
			if test.error != "" {
				assert.EqualError(t, err, test.error)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.want, readFile(t, dir, "config.toml"))
		})
	}
}

func TestInitReportsMissingVariables(t *testing.T) {
	repo := newRepo(t, map[string]fstest.MapFS{"required": {"scaffold.toml": {Data: []byte(`
[[variables]]
name = "platform"
help = "Deploy platform."
choices = ["ski"]
`)}}})
	err := scaffold.Init(t.Context(), io.Discard, scaffold.Config{Dir: t.TempDir(), Template: "required", Repo: repo})

	assert.EqualError(t, err, "template \"required\" needs values for:\n"+
		"  --var platform=VALUE  Deploy platform. One of: ski.")

	err = scaffold.Init(t.Context(), io.Discard, scaffold.Config{Dir: t.TempDir(), Template: "required", Repo: repo, Vars: map[string]string{"platform": "k8s"}})
	assert.EqualError(t, err, `platform: "k8s" is not one of ski`)
}

func TestInitErrors(t *testing.T) {
	tests := []struct {
		name   string
		config scaffold.Config
		error  string
	}{
		{name: "NoTemplate", config: scaffold.Config{Repo: "REPO"}, error: "no template given; pass --template"},
		{name: "NoRepo", config: scaffold.Config{Template: "app"}, error: "no template repository; pass --template-repo, or build spectre with spectre.WithTemplateRepo"},
		{name: "InvalidName", config: scaffold.Config{Template: "../app", Repo: "REPO"}, error: `template name "../app" must match ^[a-z0-9][a-z0-9_-]*$`},
		{name: "UnknownTemplate", config: scaffold.Config{Template: "nope", Repo: "REPO", Ref: "main"}, error: `template "nope" not found in REPO at main`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := appRepo(t)
			config := test.config
			config.Dir = t.TempDir()
			config.Repo = strings.ReplaceAll(config.Repo, "REPO", repo)
			err := scaffold.Init(t.Context(), io.Discard, config)
			assert.EqualError(t, err, strings.ReplaceAll(test.error, "REPO", repo))
		})
	}
}

// answers is the [init] table init writes into the migration config.
func answers(template string, variables ...string) string {
	return "[init]\n# Written by spectre init, which reads it on the next run. Change it with --var.\n" +
		"template = \"" + template + "\"\n\n[init.variables]\n" + strings.Join(variables, "\n") + "\n"
}

func runInit(t *testing.T, config scaffold.Config, repo string) string {
	t.Helper()
	var out strings.Builder
	config.Repo = repo
	assert.NoError(t, scaffold.Init(t.Context(), &out, config))
	return out.String()
}

// readTree reads every file below dir, marking executables and symlinks.
func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	if _, err := os.Stat(dir); err != nil {
		return tree
	}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if entry.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			tree[name] = "link:" + target
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		prefix := ""
		if info.Mode()&0o111 != 0 {
			prefix = "exec:"
		}
		tree[name] = prefix + string(content)
		return err
	})
	assert.NoError(t, err)
	return tree
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, name))
	assert.NoError(t, err)
	return string(content)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	assert.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}

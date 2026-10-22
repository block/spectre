package scaffold_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/scaffold"
	"github.com/block/spectre/internal/terminal"
)

const appManifest = `
description = "App."
next_steps = "Edit {{ .name }}."

[[variables]]
name = "name"
help = "Service name."
default = "{{ .Dir }}"

[[variables]]
name = "envs"
help = "Environments."
type = "list"
default = "staging"
choices = ["staging", "production"]

[files]
customisable = ["config.toml", "custom/**"]
executable = ["bin/*"]

[symlinks]
"link/{{ .name }}" = "../bin/run"
`

func appTemplate() fstest.MapFS {
	return fstest.MapFS{
		"scaffold.toml":          {Data: []byte(appManifest)},
		"config.toml.tmpl":       {Data: []byte("name = \"{{ .name }}\"\nenvs = \"{{ join \",\" .envs }}\"\n")},
		"bin/run":                {Data: []byte("#!/bin/sh\necho {{ .name }}\n")},
		"custom/{{ .name }}.txt": {Data: []byte("{{ .name }}\n")},
		"placeholder.txt":        {Data: []byte("placeholder\n")},
		"README.md":              {Data: []byte("# {{ .name }}\n")},
	}
}

// nextTemplate is a later app template that drops placeholder.txt and manages config.toml.
func nextTemplate() fstest.MapFS {
	files := appTemplate()
	delete(files, "placeholder.txt")
	delete(files, "config.toml.tmpl")
	manifest := strings.Replace(appManifest, `customisable = ["config.toml", "custom/**"]`, `customisable = ["custom/**"]`, 1)
	manifest = strings.Replace(manifest, "[files]", `[[variables]]
name = "image"
help = "Image."
default = "{{ .name }}-image"

[files]`, 1)
	files["scaffold.toml"] = &fstest.MapFile{Data: []byte(manifest)}
	files["config.toml"] = &fstest.MapFile{Data: []byte("image = \"{{ .image }}\"\n")}
	files["next.txt"] = &fstest.MapFile{Data: []byte("{{ .image }} {{ join \",\" .envs }}\n")}
	return files
}

func newRegistry() *scaffold.Registry {
	registry := scaffold.NewRegistry()
	registry.Register("app", appTemplate())
	registry.Register("next", nextTemplate())
	return registry
}

func TestInitWritesTemplate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "svc")
	result := runInit(t, scaffold.Config{Dir: dir, Template: "app"}, newRegistry())

	assert.Equal(t, map[string]string{
		"README.md":              "# svc\n",
		"bin/run":                "exec:#!/bin/sh\necho svc\n",
		"config.toml":            "name = \"svc\"\nenvs = \"staging\"\n",
		"custom/svc.txt":         "svc\n",
		"link/svc":               "link:../bin/run",
		"placeholder.txt":        "placeholder\n",
		"spectre/init.lock.toml": readFile(t, dir, "spectre/init.lock.toml"),
	}, readTree(t, dir))
	assert.Equal(t, scaffold.Result{
		Template: "app",
		Changes: []scaffold.Change{
			{Path: "README.md", Action: scaffold.Create},
			{Path: "bin/run", Action: scaffold.Create},
			{Path: "config.toml", Action: scaffold.Create},
			{Path: "custom/svc.txt", Action: scaffold.Create},
			{Path: "link/svc", Action: scaffold.Create},
			{Path: "placeholder.txt", Action: scaffold.Create},
		},
		NextSteps: "Edit svc.",
	}, result)
}

func TestInitRerunReconciles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "svc")
	registry := newRegistry()
	runInit(t, scaffold.Config{Dir: dir, Template: "app", Vars: map[string]string{"name": "first"}}, registry)
	writeFile(t, dir, "config.toml", "edited\n")
	writeFile(t, dir, "README.md", "edited\n")
	writeFile(t, dir, "own.txt", "the service's own file\n")

	result := runInit(t, scaffold.Config{Dir: dir, Template: "next"}, registry)

	assert.Equal(t, map[string]string{
		"README.md":              "# first\n",
		"bin/run":                "exec:#!/bin/sh\necho first\n",
		"next.txt":               "first-image staging\n",
		"config.toml":            "image = \"first-image\"\n",
		"custom/first.txt":       "first\n",
		"link/first":             "link:../bin/run",
		"own.txt":                "the service's own file\n",
		"spectre/init.lock.toml": readFile(t, dir, "spectre/init.lock.toml"),
	}, readTree(t, dir))
	assert.Equal(t, []scaffold.Change{
		{Path: "README.md", Action: scaffold.Update},
		{Path: "bin/run", Action: scaffold.Unchanged},
		{Path: "config.toml", Action: scaffold.Update},
		{Path: "custom/first.txt", Action: scaffold.Unchanged},
		{Path: "link/first", Action: scaffold.Unchanged},
		{Path: "next.txt", Action: scaffold.Create},
		{Path: "placeholder.txt", Action: scaffold.Delete},
	}, result.Changes)
}

func TestInitRerunKeepsCustomisableAndDeletesStale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "svc")
	registry := newRegistry()
	runInit(t, scaffold.Config{Dir: dir, Template: "app", Vars: map[string]string{"name": "first"}}, registry)
	writeFile(t, dir, "config.toml", "edited\n")

	result := runInit(t, scaffold.Config{Dir: dir, Vars: map[string]string{"name": "second"}}, registry)

	assert.Equal(t, map[string]string{
		"README.md":              "# second\n",
		"bin/run":                "exec:#!/bin/sh\necho second\n",
		"config.toml":            "edited\n",
		"custom/first.txt":       "first\n",
		"custom/second.txt":      "second\n",
		"link/second":            "link:../bin/run",
		"placeholder.txt":        "placeholder\n",
		"spectre/init.lock.toml": readFile(t, dir, "spectre/init.lock.toml"),
	}, readTree(t, dir))
	assert.Equal(t, []scaffold.Change{
		{Path: "README.md", Action: scaffold.Update},
		{Path: "bin/run", Action: scaffold.Update},
		{Path: "config.toml", Action: scaffold.Keep},
		{Path: "custom/second.txt", Action: scaffold.Create},
		{Path: "link/second", Action: scaffold.Create},
		{Path: "placeholder.txt", Action: scaffold.Unchanged},
		{Path: "link/first", Action: scaffold.Delete},
	}, result.Changes)
}

func TestInitDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	result := runInit(t, scaffold.Config{Dir: dir, Template: "app", DryRun: true}, newRegistry())

	assert.Equal(t, map[string]string{}, readTree(t, dir))
	assert.True(t, result.DryRun)
	assert.Equal(t, 6, len(result.Changes))
}

func TestInitVariables(t *testing.T) {
	tests := []struct {
		name   string
		config scaffold.Config
		want   string
		error  string
	}{
		{name: "Defaults", want: "name = \"svc\"\nenvs = \"staging\"\n"},
		{name: "Flags", config: scaffold.Config{Vars: map[string]string{"name": "flag", "envs": "staging, production"}}, want: "name = \"flag\"\nenvs = \"staging,production\"\n"},
		{name: "UnknownVariable", config: scaffold.Config{Vars: map[string]string{"nope": "x"}}, error: `template "app" has no variable "nope"; run spectre templates app`},
		{name: "InvalidChoice", config: scaffold.Config{Vars: map[string]string{"envs": "dev"}}, error: `envs: "dev" is not one of staging, production`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "svc")
			config := test.config
			config.Dir = dir
			config.Template = "app"
			err := scaffold.Init(&bytes.Buffer{}, config, newRegistry())
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
	registry := scaffold.NewRegistry()
	registry.Register("required", fstest.MapFS{"scaffold.toml": {Data: []byte(`
description = "Required."

[[variables]]
name = "platform"
help = "Deploy platform."
`)}})
	err := scaffold.Init(&bytes.Buffer{}, scaffold.Config{Dir: t.TempDir(), Template: "required"}, registry)

	assert.EqualError(t, err, "template \"required\" needs values for:\n"+
		"  --var platform=<string>  Deploy platform.")
}

func TestInitNeedsTemplateOnFirstRun(t *testing.T) {
	err := scaffold.Init(&bytes.Buffer{}, scaffold.Config{Dir: t.TempDir()}, newRegistry())

	assert.EqualError(t, err, "no template given; pass --template with one of: app, base, next")
}

func TestInitRendersBaseTemplate(t *testing.T) {
	dir := t.TempDir()
	runInit(t, scaffold.Config{Dir: dir, Template: scaffold.BaseTemplate}, scaffold.NewRegistry())

	assert.Equal(t, []string{"spectre/TODO.md", "spectre/init.lock.toml"}, slices.Sorted(maps.Keys(readTree(t, dir))))
}

func TestDescribeJSON(t *testing.T) {
	var out bytes.Buffer
	err := scaffold.Describe(&out, scaffold.DescribeConfig{Name: "next", JSON: true}, newRegistry(), terminal.NewPalette(false))
	assert.NoError(t, err)

	var description map[string]any
	assert.NoError(t, json.Unmarshal(out.Bytes(), &description))
	assert.Equal(t, map[string]any{
		"name":        "next",
		"description": "App.",
		"variables": []any{
			map[string]any{"name": "name", "help": "Service name.", "type": "string", "default": "{{ .Dir }}"},
			map[string]any{"name": "envs", "help": "Environments.", "type": "list", "default": "staging", "choices": []any{"staging", "production"}},
			map[string]any{"name": "image", "help": "Image.", "type": "string", "default": "{{ .name }}-image"},
		},
	}, description)
}

func runInit(t *testing.T, config scaffold.Config, registry *scaffold.Registry) scaffold.Result {
	t.Helper()
	config.JSON = true
	var out bytes.Buffer
	assert.NoError(t, scaffold.Init(&out, config, registry))
	var result scaffold.Result
	assert.NoError(t, json.Unmarshal(out.Bytes(), &result))
	return result
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

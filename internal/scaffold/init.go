// Package scaffold sets a service up for SPECTRE by rendering a template from a
// template repository into the spectre directory of its checkout with scaffolder.
//
// A template's template.js runs before anything is written, so it can check the
// variables and throw to stop the run. The spectre_version variable defaults to
// the git tag or commit of the SPECTRE this binary is built from.
package scaffold

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/block/scaffolder"
	"github.com/block/scaffolder/extensions/javascript"
	"golang.org/x/mod/module"
)

// Config selects the template init renders and its variables.
type Config struct {
	// Dir is the service checkout to set up.
	Dir string `arg:"" optional:"" default:"." type:"existingdir" help:"Service checkout to set up. The template is rendered into its spectre directory."`
	// Template names the template to render.
	Template string `required:"" placeholder:"NAME" help:"Template to render, a directory at the root of the template repository."`
	// Repo holds the templates, one directory each.
	Repo string `name:"template-repo" default:"${template_repo}" placeholder:"SOURCE" help:"Template repository: a local directory, or a git source such as git+ssh://github.com/org/repo."`
	// Force renders over an existing spectre directory.
	Force bool `short:"f" help:"Render over an existing spectre directory, overwriting the files the template writes."`
	// Vars are the template's variables.
	Vars map[string]string `name:"var" mapsep:"none" placeholder:"NAME=VALUE" help:"Set a template variable. Write a list comma-separated. spectre_version defaults to the SPECTRE version of this binary."`
}

// Init renders the template into the spectre directory of config.Dir, which
// must not exist yet unless config.Force is set.
func Init(ctx context.Context, config Config) error {
	if config.Repo == "" {
		return errors.New("no template repository; pass --template-repo, or build spectre with spectre.WithTemplateRepo")
	}
	target := filepath.Join(config.Dir, "spectre")
	if _, err := os.Stat(target); err == nil && !config.Force {
		return errors.Errorf("%s already exists; pass --force to overwrite it", target)
	}
	files, cleanup, err := scaffolder.Open(ctx, config.Repo+"//"+config.Template)
	if err != nil {
		return errors.Wrapf(err, "fetch template %q", config.Template)
	}
	defer cleanup()
	vars := maps.Clone(config.Vars)
	if vars == nil {
		vars = map[string]string{}
	}
	if _, set := vars["spectre_version"]; !set {
		if info, ok := debug.ReadBuildInfo(); ok {
			if version, known := spectreVersion(info); known {
				vars["spectre_version"] = version
			}
		}
	}
	err = scaffolder.Scaffold(files, target, vars,
		scaffolder.WithContext(ctx),
		scaffolder.Functions(scaffolder.FuncMap{"split": split}),
		scaffolder.Extend(javascript.Extension("template.js")),
	)
	return errors.Wrapf(err, "render template %q", config.Template)
}

// spectreVersion returns the git tag or commit of the SPECTRE module in a build,
// whether it is the main module or a dependency.
func spectreVersion(info *debug.BuildInfo) (version string, ok bool) {
	const path = "github.com/block/spectre"
	modules := append([]*debug.Module{&info.Main}, info.Deps...)
	for _, mod := range modules {
		if mod.Path != path {
			continue
		}
		if mod.Replace != nil {
			mod = mod.Replace
		}
		version, _, _ = strings.Cut(mod.Version, "+")
		if module.IsPseudoVersion(version) {
			rev, err := module.PseudoVersionRev(version)
			return rev, err == nil
		}
		if !strings.HasPrefix(version, "v") {
			return "", false
		}
		return version, true
	}
	return "", false
}

// split returns the comma-separated items of a value, trimmed, without empty ones.
func split(value string) []string {
	items := []string{}
	for item := range strings.SplitSeq(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

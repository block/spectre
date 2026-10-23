// Package scaffold sets a service up for SPECTRE by rendering a template from a
// template repository into the spectre directory of its checkout with scaffolder.
//
// A template's template.js runs before anything is written, so it can check the
// variables and throw to stop the run.
package scaffold

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/block/scaffolder"
	"github.com/block/scaffolder/extensions/javascript"
)

// Config selects the template init renders and its variables.
type Config struct {
	// Dir is the service checkout to set up.
	Dir string `arg:"" optional:"" default:"." type:"existingdir" help:"Service checkout to set up. The template is rendered into its spectre directory."`
	// Template names the template to render.
	Template string `required:"" placeholder:"NAME" help:"Template to render, a directory at the root of the template repository."`
	// Repo holds the templates, one directory each.
	Repo string `name:"template-repo" default:"${template_repo}" placeholder:"SOURCE" help:"Template repository: a local directory, or a git source such as git+ssh://github.com/org/repo."`
	// Vars are the template's variables.
	Vars map[string]string `name:"var" mapsep:"none" placeholder:"NAME=VALUE" help:"Set a template variable. Write a list comma-separated."`
}

// Init renders the template into the spectre directory of config.Dir, which
// must not exist yet.
func Init(ctx context.Context, config Config) error {
	if config.Repo == "" {
		return errors.New("no template repository; pass --template-repo, or build spectre with spectre.WithTemplateRepo")
	}
	target := filepath.Join(config.Dir, "spectre")
	if _, err := os.Stat(target); err == nil {
		return errors.Errorf("%s already exists", target)
	}
	files, cleanup, err := scaffolder.Open(ctx, config.Repo+"//"+config.Template)
	if err != nil {
		return errors.Wrapf(err, "fetch template %q", config.Template)
	}
	defer cleanup()
	err = scaffolder.Scaffold(files, target, config.Vars,
		scaffolder.WithContext(ctx),
		scaffolder.Functions(scaffolder.FuncMap{"split": split}),
		scaffolder.Extend(javascript.Extension("template.js")),
	)
	return errors.Wrapf(err, "render template %q", config.Template)
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

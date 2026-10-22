// Package scaffold writes the files a SPECTRE migration needs into a service
// checkout from a template, and keeps them up to date on later runs.
//
// A template is a file tree rendered with scaffolder, with a manifest at its
// root that declares its variables and which files are customisable. A run
// deletes the files in spectre/ that are not customisable, then renders the
// template, except customisable files already there. The answers are kept in
// spectre/migration.toml.
package scaffold

import (
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
)

// Config selects the template init renders and where its values come from.
type Config struct {
	// Dir is the service checkout to write into.
	Dir string `arg:"" optional:"" default:"." type:"path" help:"Service checkout to write the files into."`
	// Template names the template to render.
	Template string `placeholder:"NAME" help:"Template to render, a directory at the root of the template repository. Defaults to the one the last run used."`
	// Repo holds the templates, one directory each.
	Repo string `name:"template-repo" default:"${template_repo}" placeholder:"SOURCE" help:"Template repository: a local directory, or a git source such as git+ssh://github.com/org/repo."`
	// Ref is the branch or tag of a git template repository.
	Ref string `name:"template-ref" default:"main" placeholder:"REF" help:"Branch or tag of the template repository."`
	// Vars are variable values, as written on the command line.
	Vars map[string]string `name:"var" mapsep:"none" placeholder:"NAME=VALUE" help:"Set a template variable. Write a list comma-separated."`
}

// Init renders a template into config.Dir and writes its next steps to out.
func Init(ctx context.Context, out io.Writer, config Config) error {
	dir, err := filepath.Abs(config.Dir)
	if err != nil {
		return errors.WithStack(err)
	}
	previous, found, err := readAnswers(dir)
	if err != nil {
		return err
	}
	name := config.Template
	if name == "" {
		if !found {
			return errors.New("no template given; pass --template")
		}
		name = previous.Template
	}
	template, cleanup, err := openTemplate(ctx, config, name)
	if err != nil {
		return err
	}
	defer cleanup()
	builtins := map[string]any{"Dir": filepath.Base(dir), "SpectreRef": spectreRef(), "Template": name}
	values, err := resolve(template, config.Vars, previous.Variables)
	if err != nil {
		return err
	}
	context := merge(builtins, values)
	kept, err := clean(dir, template.Manifest.Files.Customisable)
	if err != nil {
		return err
	}
	if err := template.Render(dir, context, kept); err != nil {
		return err
	}
	if err := writeAnswers(dir, newAnswers(name, values)); err != nil {
		return err
	}
	steps, err := template.NextSteps(context)
	if err != nil || steps == "" {
		return err
	}
	_, err = fmt.Fprintf(out, "Next steps:\n%s\n", strings.TrimRight(steps, "\n"))
	return errors.Wrap(err, "write next steps")
}

// resolve finds each variable's value, from flags, then the previous run.
func resolve(template Template, flags, locked map[string]string) (map[string]string, error) {
	names := make([]string, 0, len(template.Manifest.Variables))
	for _, variable := range template.Manifest.Variables {
		names = append(names, variable.Name)
	}
	for _, name := range slices.Sorted(maps.Keys(flags)) {
		if !slices.Contains(names, name) {
			return nil, errors.Errorf("template %q has no variable %q; it has: %s", template.Name, name, strings.Join(names, ", "))
		}
	}
	values := map[string]string{}
	var missing []Variable
	for _, variable := range template.Manifest.Variables {
		value, ok := locked[variable.Name]
		if ok && variable.check(value) != nil {
			ok = false // A recorded answer the template no longer accepts is missing.
		}
		if flag, found := flags[variable.Name]; found {
			value, ok = flag, true
		}
		if !ok {
			missing = append(missing, variable)
			continue
		}
		if err := variable.check(value); err != nil {
			return nil, err
		}
		values[variable.Name] = value
	}
	if len(missing) > 0 {
		return nil, missingError(template.Name, missing)
	}
	return values, nil
}

func missingError(template string, missing []Variable) error {
	var message strings.Builder
	fmt.Fprintf(&message, "template %q needs values for:", template)
	for _, variable := range missing {
		fmt.Fprintf(&message, "\n  --var %s=VALUE  %s", variable.Name, variable.Help)
		if len(variable.Choices) > 0 {
			fmt.Fprintf(&message, " One of: %s.", strings.Join(variable.Choices, ", "))
		}
	}
	return errors.New(message.String())
}

// merge returns the built-in values with the variable values added.
func merge(builtins map[string]any, values map[string]string) map[string]any {
	merged := maps.Clone(builtins)
	for name, value := range values {
		merged[name] = value
	}
	return merged
}

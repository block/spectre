// Package scaffold writes the files a SPECTRE migration needs into a service
// checkout from a template, and keeps them up to date on later runs.
//
// A template is a file tree rendered with scaffolder, with a manifest at its
// root that declares its variables and which files are customisable. A run
// overwrites the files it renders, except customisable ones, and deletes every
// other file in spectre/. The answers are kept in spectre/migration.toml.
package scaffold

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// Config selects the template init renders and where its values come from.
type Config struct {
	// Dir is the service checkout to write into.
	Dir string `arg:"" optional:"" default:"." type:"path" help:"Service checkout to write the files into."`
	// Template names the template to render.
	Template string `placeholder:"NAME" help:"Template to render. Defaults to the one the last run used."`
	// Vars are variable values, as written on the command line.
	Vars map[string]string `name:"var" mapsep:"none" placeholder:"NAME=VALUE" help:"Set a template variable. Write a list comma-separated."`
	// DryRun reports the changes without writing them.
	DryRun bool `help:"Report the changes without writing anything."`
	// JSON reports the changes as JSON.
	JSON bool `help:"Report the changes as JSON."`
}

// Result is what a run of init did, or would do in a dry run.
type Result struct {
	// Template is the rendered template's name.
	Template string `json:"template"`
	// DryRun is set when nothing was written.
	DryRun bool `json:"dry_run"`
	// Changes are the actions on each file, including unchanged ones.
	Changes []Change `json:"changes"`
	// NextSteps are the template's instructions for what to do after init.
	NextSteps string `json:"next_steps,omitzero"`
}

// Init renders a template into config.Dir and reports the changes to out.
func Init(out io.Writer, config Config, registry *Registry) error {
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
			return errors.Errorf("no template given; pass --template with one of: %s", strings.Join(registry.Names(), ", "))
		}
		name = previous.Template
	}
	template, err := registry.Load(name)
	if err != nil {
		return err
	}
	builtins := map[string]any{"Dir": filepath.Base(dir), "SpectreRef": spectreRef(), "Template": name}
	values, err := resolve(template, builtins, config.Vars, previous.Variables)
	if err != nil {
		return err
	}
	context := merge(builtins, values)
	files, err := template.Render(context)
	if err != nil {
		return err
	}
	if err := recordAnswers(dir, files, newAnswers(name, values)); err != nil {
		return err
	}
	changes, err := plan(dir, files, template.Manifest.Files.Customisable)
	if err != nil {
		return err
	}
	if !config.DryRun {
		if err := apply(dir, files, changes); err != nil {
			return err
		}
	}
	steps, err := template.NextSteps(context)
	if err != nil {
		return err
	}
	return report(out, Result{Template: name, DryRun: config.DryRun, Changes: changes, NextSteps: steps}, config.JSON)
}

// resolve finds each variable's value, from flags, then the previous run, then
// the default.
func resolve(template Template, builtins map[string]any, flags, locked map[string]string) (map[string]any, error) {
	declared := map[string]bool{}
	for _, variable := range template.Manifest.Variables {
		declared[variable.Name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(flags)) {
		if !declared[name] {
			return nil, errors.Errorf("template %q has no variable %q; run spectre templates %s", template.Name, name, template.Name)
		}
	}
	values := map[string]any{}
	var missing []Variable
	for _, variable := range template.Manifest.Variables {
		value, ok, err := given(variable, flags, locked)
		if err != nil {
			return nil, err
		}
		if !ok {
			fallback, found := renderDefault(variable, builtins, values).Get()
			if !found {
				missing = append(missing, variable)
				continue
			}
			if value, err = variable.parse(fallback); err != nil {
				return nil, errors.Wrap(err, "default")
			}
		}
		values[variable.Name] = value
	}
	if len(missing) > 0 {
		return nil, missingError(template.Name, missing)
	}
	return values, nil
}

// given returns a variable's value from flags or the previous run. A previous
// value the variable no longer accepts is asked for again.
func given(variable Variable, flags, locked map[string]string) (value any, ok bool, err error) {
	if raw, found := flags[variable.Name]; found {
		return checked(variable)(variable.parse(raw))
	}
	raw, found := locked[variable.Name]
	if !found {
		return nil, false, nil
	}
	value, ok, err = checked(variable)(variable.parse(raw))
	if err != nil {
		return nil, false, nil //nolint:nilerr // A stale answer is asked for again.
	}
	return value, ok, nil
}

// checked returns a function that checks a parsed value against variable.
func checked(variable Variable) func(value any, err error) (any, bool, error) {
	return func(value any, err error) (any, bool, error) {
		if err == nil {
			err = variable.check(value)
		}
		if err != nil {
			return nil, false, err
		}
		return value, true, nil
	}
}

// renderDefault renders a variable's default from the built-in values and the
// earlier variables. A default that refers to a missing value has none.
func renderDefault(variable Variable, builtins, values map[string]any) Option[string] {
	raw, ok := variable.Default.Get()
	if !ok {
		return None[string]()
	}
	rendered, err := renderText(variable.Name+" default", raw, merge(builtins, values))
	if err != nil {
		return None[string]()
	}
	return Some(rendered)
}

func missingError(template string, missing []Variable) error {
	var message strings.Builder
	fmt.Fprintf(&message, "template %q needs values for:", template)
	for _, variable := range missing {
		fmt.Fprintf(&message, "\n  --var %s=<%s>  %s", variable.Name, variable.Type, variable.Help)
	}
	return errors.New(message.String())
}

func report(out io.Writer, result Result, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return errors.Wrap(encoder.Encode(result), "write init result")
	}
	var text bytes.Buffer
	if result.DryRun {
		text.WriteString("Dry run: no files were written.\n")
	}
	unchanged := 0
	for _, change := range result.Changes {
		if change.Action == Unchanged {
			unchanged++
			continue
		}
		fmt.Fprintf(&text, "%-9s %s\n", change.Action, change.Path)
	}
	fmt.Fprintf(&text, "%d unchanged\n", unchanged)
	if result.NextSteps != "" {
		fmt.Fprintf(&text, "\nNext steps:\n%s\n", strings.TrimRight(result.NextSteps, "\n"))
	}
	_, err := out.Write(text.Bytes())
	return errors.Wrap(err, "write init result")
}

// merge returns the built-in values with the variable values added.
func merge(builtins, values map[string]any) map[string]any {
	merged := maps.Clone(builtins)
	maps.Copy(merged, values)
	return merged
}

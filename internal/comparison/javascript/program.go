// Package javascript evaluates Spectre payload normaliser scripts.
package javascript

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/schema"
)

const (
	spectreModuleName   = "spectre"
	typeScriptExtension = ".ts"
	// entryModuleName names the synthetic module importing every script. It cannot
	// collide with a script, whose names end in ".ts".
	entryModuleName = "."
)

// Program is one immutable set of scripts, shared by all payload normalisations.
// It retains no runtime-local callback state.
type Program struct {
	schema    *schema.Schema
	entry     *sobek.SourceTextModuleRecord
	spectre   *spectreModule
	endpoints []Endpoint
	fields    []FieldTarget
	messages  []string
}

// NewProgram type-checks every TypeScript script against the schema declarations,
// links the scripts, then evaluates them once to validate their combined registrations.
func NewProgram(ctx context.Context, declarations fs.FS, scripts fs.FS) (*Program, error) {
	declarationSources, err := schema.ReadSources(declarations)
	if err != nil {
		return nil, errors.Wrap(err, "read schema declarations")
	}
	scriptSources, err := readScripts(scripts)
	if err != nil {
		return nil, err
	}
	checked, err := compile(ctx, declarationSources, scriptSources)
	if err != nil {
		return nil, err
	}
	loader := newModuleLoader(checked.modules)
	module, err := loader.loadAll()
	if err != nil {
		return nil, err
	}
	if err := module.Link(); err != nil {
		return nil, errors.Wrap(err, "link comparison modules")
	}
	evaluator, registered, err := newEvaluator(ctx, module, loader.spectreModule())
	if err != nil {
		return nil, errors.Wrap(err, "evaluate comparison modules")
	}
	defer evaluator.Close()
	return &Program{
		schema:    checked.schema,
		entry:     module,
		spectre:   loader.spectreModule(),
		endpoints: registered.endpoints,
		fields:    registered.fields,
		messages:  registered.messages,
	}, nil
}

// readScripts reads TypeScript sources and declarations. Symlinks are rejected
// because fs.FS provides no portable root-confined open operation.
func readScripts(scripts fs.FS) (map[string]string, error) {
	sources := map[string]string{}
	err := fs.WalkDir(scripts, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return errors.Wrap(err, "read scripts directory")
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return errors.Errorf("comparison script path %q is a symbolic link", name)
		}
		if path.Ext(name) == ".js" {
			return errors.Errorf("comparison script %q is JavaScript; scripts must be TypeScript", name)
		}
		if entry.IsDir() || path.Ext(name) != typeScriptExtension {
			return nil
		}
		source, err := fs.ReadFile(scripts, name)
		if err != nil {
			return errors.Wrapf(err, "read comparison script %q", name)
		}
		sources[name] = string(source)
		return nil
	})
	return sources, errors.WithStack(err)
}

// Schema returns the payload types the scripts were checked against.
func (p *Program) Schema() *schema.Schema {
	return p.schema
}

// Endpoints returns the endpoints declared for a direction, in declaration order.
func (p *Program) Endpoints(direction Direction) []Endpoint {
	endpoints := []Endpoint{}
	for _, endpoint := range p.endpoints {
		if endpoint.Direction() == direction {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints
}

// Fields returns the declared type names and JSON field paths, sorted by type then path.
func (p *Program) Fields() []FieldTarget {
	return slices.Clone(p.fields)
}

// Messages returns the object type names declared by the scripts.
func (p *Program) Messages() []string {
	return slices.Clone(p.messages)
}

// NewEvaluator creates an isolated evaluator for one payload normalisation.
// Re-evaluation must reproduce the registrations used to build the schema plan.
func (p *Program) NewEvaluator(ctx context.Context) (*Evaluator, error) {
	evaluator, registered, err := newEvaluator(ctx, p.entry, p.spectre)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(registered.endpoints, p.endpoints) ||
		!slices.Equal(registered.fields, p.fields) ||
		!slices.Equal(registered.messages, p.messages) {
		evaluator.Close()
		return nil, errors.New("comparison script registrations changed after startup")
	}
	return evaluator, nil
}

// moduleLoader parses each compiled module once per program, so a library imported
// along several paths is one module record and is evaluated once per runtime.
type moduleLoader struct {
	compiled map[string]string
	spectre  *spectreModule
	modules  map[string]*sobek.SourceTextModuleRecord
	names    map[sobek.ModuleRecord]string
}

func newModuleLoader(compiled map[string]string) *moduleLoader {
	return &moduleLoader{
		compiled: compiled,
		spectre:  &spectreModule{},
		modules:  map[string]*sobek.SourceTextModuleRecord{},
		names:    map[sobek.ModuleRecord]string{},
	}
}

func (l *moduleLoader) spectreModule() *spectreModule {
	return l.spectre
}

// loadAll parses a synthetic entry module that imports every script, so the set
// links and evaluates as one module graph in which each script runs once.
func (l *moduleLoader) loadAll() (*sobek.SourceTextModuleRecord, error) {
	var source strings.Builder
	// Importing spectre gives every runtime a registry, even with no scripts.
	fmt.Fprintf(&source, "import %q;\n", spectreModuleName)
	for _, name := range slices.Sorted(maps.Keys(l.compiled)) {
		// JSON strings are valid JavaScript string literals.
		specifier, err := json.Marshal("./" + name)
		if err != nil {
			return nil, errors.Wrapf(err, "encode comparison module name %q", name)
		}
		fmt.Fprintf(&source, "import %s;\n", specifier)
	}
	module, err := sobek.ParseModule(entryModuleName, source.String(), l.resolve)
	if err != nil {
		return nil, errors.Wrap(err, "parse comparison entry module")
	}
	l.names[module] = entryModuleName
	return module, nil
}

func (l *moduleLoader) load(name string) (*sobek.SourceTextModuleRecord, error) {
	if module, ok := l.modules[name]; ok {
		return module, nil
	}
	module, err := sobek.ParseModule(name, l.compiled[name], l.resolve)
	if err != nil {
		return nil, errors.Wrapf(err, "parse comparison module %q", name)
	}
	l.modules[name] = module
	l.names[module] = name
	return module, nil
}

// resolve confines imports to compiled scripts; transpilation erases type-only schema
// imports. Sobek links the whole graph with the entry module's resolver.
func (l *moduleLoader) resolve(referencing any, specifier string) (sobek.ModuleRecord, error) {
	if specifier == spectreModuleName {
		return l.spectre, nil
	}
	module, _ := referencing.(sobek.ModuleRecord)
	referrer, ok := l.names[module]
	if !ok {
		return nil, errors.Errorf("import %q has no referencing comparison module", specifier)
	}
	if !strings.HasPrefix(specifier, "./") && !strings.HasPrefix(specifier, "../") {
		return nil, errors.Errorf("comparison module %q must import modules by relative path, not %q", referrer, specifier)
	}
	name := path.Join(path.Dir(referrer), specifier)
	if !fs.ValidPath(name) {
		return nil, errors.Errorf("comparison module %q imports %q outside the scripts directory", referrer, specifier)
	}
	if path.Ext(name) == "" {
		name += typeScriptExtension
	}
	if _, found := l.compiled[name]; !found {
		return nil, errors.Errorf("comparison module %q imports %q, which is not a TypeScript script", referrer, specifier)
	}
	return l.load(name)
}

func isScript(name string) bool {
	return path.Ext(name) == typeScriptExtension && !strings.HasSuffix(name, ".d.ts")
}

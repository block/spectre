// Package javascript evaluates Spectre payload normaliser scripts.
package javascript

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/grafana/sobek"
)

const (
	spectreModuleName = "spectre"
	// entryModuleName names the synthetic module importing every script. It cannot
	// collide with a script, whose names end in ".js".
	entryModuleName = "."
)

// Program is one immutable set of scripts, shared by all payload normalisations.
// It retains no runtime-local callback state.
type Program struct {
	entry     *sobek.SourceTextModuleRecord
	spectre   *spectreModule
	endpoints []Endpoint
	fields    []string
	messages  []string
}

// NewProgram parses and links every .js file in scripts, then evaluates them once
// in one runtime to validate their combined registrations.
func NewProgram(ctx context.Context, scripts fs.FS) (*Program, error) {
	loader := newModuleLoader(scripts)
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
		entry:     module,
		spectre:   loader.spectreModule(),
		endpoints: registered.endpoints,
		fields:    registered.fields,
		messages:  registered.messages,
	}, nil
}

// Endpoints returns the raw HTTP endpoints declared for a direction, in declaration order.
func (p *Program) Endpoints(direction Direction) []Endpoint {
	endpoints := []Endpoint{}
	for _, endpoint := range p.endpoints {
		if endpoint.Direction() == direction {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints
}

// Fields returns the protobuf fields declared by the scripts.
func (p *Program) Fields() []string {
	return slices.Clone(p.fields)
}

// Messages returns the protobuf messages declared by the scripts.
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

// moduleLoader parses each module file once per program, so a library imported
// along several paths is one module record and is evaluated once per runtime.
type moduleLoader struct {
	scripts fs.FS
	spectre *spectreModule
	modules map[string]*sobek.SourceTextModuleRecord
	names   map[sobek.ModuleRecord]string
}

func newModuleLoader(scripts fs.FS) *moduleLoader {
	return &moduleLoader{
		scripts: scripts,
		spectre: &spectreModule{},
		modules: map[string]*sobek.SourceTextModuleRecord{},
		names:   map[sobek.ModuleRecord]string{},
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
	err := fs.WalkDir(l.scripts, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return errors.Wrap(err, "read scripts directory")
		}
		if entry.IsDir() || path.Ext(name) != ".js" {
			return nil
		}
		// JSON strings are valid JavaScript string literals.
		specifier, err := json.Marshal("./" + name)
		if err != nil {
			return errors.Wrapf(err, "encode comparison module name %q", name)
		}
		fmt.Fprintf(&source, "import %s;\n", specifier)
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
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
	source, err := fs.ReadFile(l.scripts, name)
	if err != nil {
		return nil, errors.Wrapf(err, "read comparison module %q", name)
	}
	module, err := sobek.ParseModule(name, string(source), l.resolve)
	if err != nil {
		return nil, errors.Wrapf(err, "parse comparison module %q", name)
	}
	l.modules[name] = module
	l.names[module] = name
	return module, nil
}

// resolve confines imports to relative paths inside the scripts directory. Sobek
// links the whole graph with the entry module's resolver.
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
	return l.load(name)
}

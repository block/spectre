package script_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/http/script"
	"github.com/block/spectre/internal/schema"
)

const programDeclarations = `
declare module "test.v1" {
  export interface Response { value: string; values: string[]; owner?: string }
  export interface User { name: string }
}
`

// scriptsDeclarations mirror the ProtoJSON shape of a protobuf package, with optional
// members so tests send only what they compare.
const scriptsDeclarations = `
declare module "test.v1" {
  export interface Request {}
  export interface Query { location?: string; trace?: string; }
  export interface User { name: string; }
  export interface Response {
    stable?: string;
    ignored?: string;
    roles?: string[];
    users?: import("test.v1").User[];
    count?: string;
    labels?: Record<string, string>;
  }
  export interface Service {
    Get(request: import("test.v1").Request): import("test.v1").Response;
  }
}
`

// untyped bypasses the checker so tests can reach the runtime's own argument checks.
const untyped = `
	const { ingress, egress } = spectre as unknown as Record<string, Record<string, (...args: unknown[]) => any>>;
`

// module is a script with the spectre module and the test.v1 types imported.
func module(body string) string {
	return `import * as spectre from "spectre"; import type * as v1 from "test.v1";` + body
}

func scriptFile(body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(module(body))}
}

func newProgram(t *testing.T, body string) (*javascript.Program, script.Handle, error) {
	t.Helper()
	return compile(t, fstest.MapFS{"test.ts": scriptFile(body)})
}

func compile(t *testing.T, files fstest.MapFS) (*javascript.Program, script.Handle, error) {
	t.Helper()
	binding, handle := script.New()
	declarations := fstest.MapFS{"test.d.ts": {Data: []byte(programDeclarations)}}
	program, err := javascript.NewProgram(t.Context(), time.Second, declarations, files, nil, []javascript.Binding{binding})
	return program, handle, err
}

func newScripts(t *testing.T, files map[string]string) (*comparison.Scripts, script.Handle, error) {
	t.Helper()
	config := comparison.NewConfig()
	config.Schema.SchemaDir = writeFiles(t, map[string]string{"test.d.ts": scriptsDeclarations})
	config.ScriptsDir = writeFiles(t, files)
	binding, handle := script.New()
	scripts, err := comparison.New(t.Context(), config, []javascript.Binding{binding})
	return scripts, handle, err
}

func loadScripts(t *testing.T, body string) (*comparison.Scripts, script.Handle) {
	t.Helper()
	scripts, handle, err := newScripts(t, map[string]string{"test.ts": module(body)})
	assert.NoError(t, err)
	return scripts, handle
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, source := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		assert.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		assert.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	}
	return dir
}

type endpoint struct {
	protocol model.Protocol
	pattern  string
	typeName schema.TypeRef
}

func endpoints(program *javascript.Program, direction model.Direction) []endpoint {
	declared := []endpoint{}
	for _, value := range program.Endpoints(direction) {
		declared = append(declared, endpoint{value.Protocol(), value.Pattern(), value.Type()})
	}
	return declared
}

type rule struct {
	declared string
	action   script.Action
}

func rules(program *javascript.Program, handle script.Handle) []rule {
	declared := []rule{}
	for _, value := range handle.Declared(program).Rules {
		declared = append(declared, rule{value.String(), value.Action()})
	}
	return declared
}

// unknownValue decodes JSON as a value of type unknown, which is what normalisers return.
func unknownValue(t *testing.T, data string) schema.Value {
	t.Helper()
	return typed(t, schema.UnknownType{}, data)
}

func typed(t *testing.T, declared schema.Type, data string) schema.Value {
	t.Helper()
	value, err := schema.Decode(declared, []byte(data))
	assert.NoError(t, err)
	return value
}

func objectType(t *testing.T, program *javascript.Program, name schema.TypeRef) *schema.ObjectType {
	t.Helper()
	declared, err := program.Schema().Type(name)
	assert.NoError(t, err)
	return declared
}

// objectValue decodes JSON as a value of the named object type.
func objectValue(t *testing.T, program *javascript.Program, name schema.TypeRef, data string) schema.Value {
	t.Helper()
	return typed(t, objectType(t, program, name), data)
}

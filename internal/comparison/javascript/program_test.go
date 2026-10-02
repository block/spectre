package javascript_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/comparison/javascript"
)

const testDeclarations = `
declare module "test.v1" {
  export interface Response { value: string; values: string[]; owner?: string }
  export interface User { name: string }
  export interface UserService { GetUser(request: User): User }
}
declare module "test" {
  export interface A { B: A.B }
  export namespace A { export interface B { value: string } }
}
`

func TestProgramOwnsDeclarations(t *testing.T) {
	program, err := compile(t, fstest.MapFS{
		"weather.ts": script(`
			spectre.ingress<v1.Response>("http", "GET /v1/forecast");
			spectre.ingress<v1.Response>("http", "GET /v2/forecast");
			spectre.egress<v1.Response>("http", "GET weather.example/v1/forecast");
			spectre.field<v1.Response, "value">(() => "fixed");
		`),
		"users/user.ts": script(`spectre.message<v1.User>((user) => user);`),
	})
	assert.NoError(t, err)

	assert.Equal(t, []endpoint{{"http", "GET /v1/forecast", "test.v1.Response"}, {"http", "GET /v2/forecast", "test.v1.Response"}}, endpoints(program, javascript.Ingress))
	assert.Equal(t, []endpoint{{"http", "GET weather.example/v1/forecast", "test.v1.Response"}}, endpoints(program, javascript.Egress))
	assert.Equal(t, []javascript.FieldTarget{javascript.NewFieldTarget("test.v1.Response", "value")}, program.Fields())
	assert.Equal(t, []string{"test.v1.User"}, program.Messages())
	operation, err := program.Schema().Operation("test.v1.UserService.GetUser")
	assert.NoError(t, err)
	assert.Equal(t, "test.v1.User", operation.Request)
}

func TestLoadsEmptyScriptsDirectory(t *testing.T) {
	program, err := compile(t, fstest.MapFS{"README.md": {Data: []byte("Not a script.")}})
	assert.NoError(t, err)

	assert.Equal(t, []endpoint{}, endpoints(program, javascript.Ingress))
	assert.Equal(t, []endpoint{}, endpoints(program, javascript.Egress))
	assert.Equal(t, []javascript.FieldTarget{}, program.Fields())
	assert.Equal(t, []string{}, program.Messages())
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	evaluator.Close()
}

func TestResolvesTypeArgumentsWithTheChecker(t *testing.T) {
	program, err := compile(t, fstest.MapFS{
		"aliases.ts": {Data: []byte(`
			import { field as normalise, message } from "spectre";
			import type { User } from "test.v1";
			import * as test from "test";
			type Person = User;
			const register = message;
			register<Person>((user) => user);
			normalise<test.A, "B.value">(() => "first");
		`)},
	})
	assert.NoError(t, err)
	assert.Equal(t, []string{"test.v1.User"}, program.Messages())
	assert.Equal(t, []javascript.FieldTarget{javascript.NewFieldTarget("test.A", "B.value")}, program.Fields())
}

func TestRejectsUncheckedRegistrations(t *testing.T) {
	for name, test := range map[string]struct {
		files   fstest.MapFS
		message string
	}{
		"TypeError": {
			files:   fstest.MapFS{"test.ts": script(`const count: number = "one";`)},
			message: "scripts/test.ts(1,78): error TS2322: Type 'string' is not assignable to type 'number'.",
		},
		"MissingTypeArgument": {
			files:   fstest.MapFS{"test.ts": script(`spectre.ingress("http", "GET /v1/forecast");`)},
			message: "scripts/test.ts:1:72: spectre.ingress needs 1 explicit type argument(s)",
		},
		"MissingPath": {
			files:   fstest.MapFS{"test.ts": script(`spectre.field<v1.Response>((value) => value);`)},
			message: "Expected 2 type arguments, but got 1.",
		},
		"UnknownPath": {
			files:   fstest.MapFS{"test.ts": script(`spectre.field<v1.Response, "missing">((value) => value);`)},
			message: `Type '"missing"' does not satisfy the constraint`,
		},
		"WrongNormaliser": {
			files:   fstest.MapFS{"test.ts": script(`spectre.field<v1.Response, "value">((value: number) => value);`)},
			message: "error TS2345",
		},
		"PathUnion": {
			files:   fstest.MapFS{"test.ts": script(`spectre.field<v1.Response, "value" | "owner">(() => undefined);`)},
			message: "spectre.field path must be one string literal type",
		},
		"LocalType": {
			files:   fstest.MapFS{"test.ts": script(`interface Local { name: string } spectre.message<Local>((value) => value);`)},
			message: `spectre.message type argument Local: type "Local" is not exported from a schema module`,
		},
		"InlineType": {
			files:   fstest.MapFS{"test.ts": script(`spectre.message<{ name: string }>((value) => value);`)},
			message: "object types must be declared by name in a schema module",
		},
		"TypeParameter": {
			files:   fstest.MapFS{"test.ts": script(`function register<T extends object>() { spectre.message<T>((value) => value); }`)},
			message: "spectre.message type argument T: object types must be declared by name",
		},
		"Service": {
			files:   fstest.MapFS{"test.ts": script(`spectre.message<v1.UserService>((value) => value);`)},
			message: `type "test.v1.UserService" is not declared`,
		},
		"Augmentation": {
			files: fstest.MapFS{"test.ts": script(`
				declare module "test.v1" { interface Extra { name: string } }
				spectre.message<v1.Extra>((value) => value);
			`)},
			message: `type "Extra" is not exported from a schema module`,
		},
		"JavaScript": {
			files:   fstest.MapFS{"test.js": {Data: []byte(`export const value = 1;`)}},
			message: `comparison script "test.js" is JavaScript; scripts must be TypeScript`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(t, test.files)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestRejectsInvalidEndpointDeclarations(t *testing.T) {
	for name, test := range map[string]struct {
		body    string
		message string
	}{
		"NoArguments": {body: `ingress();`, message: "requires a type name, a protocol, and a pattern"},
		"TwoArguments": {
			body:    `ingress("test.v1.Response", "http");`,
			message: "requires a type name, a protocol, and a pattern",
		},
		"FourArguments": {
			body:    `ingress("test.v1.Response", "http", "GET /v1/forecast", 1);`,
			message: "requires a type name, a protocol, and a pattern",
		},
		"EmptyType":         {body: `ingress("", "http", "GET /v1/forecast");`, message: "type name must be a non-empty string"},
		"NonStringType":     {body: `ingress({}, "http", "GET /v1/forecast");`, message: "type name must be a non-empty string"},
		"EmptyProtocol":     {body: `ingress("test.v1.Response", "", "GET /v1/forecast");`, message: "protocol must be a non-empty string"},
		"NonStringProtocol": {body: `ingress("test.v1.Response", 1, "GET /v1/forecast");`, message: "protocol must be a non-empty string"},
		"EmptyPattern":      {body: `ingress("test.v1.Response", "http", "");`, message: "pattern must be a non-empty string"},
		"NonStringPattern":  {body: `ingress("test.v1.Response", "http", 1);`, message: "pattern must be a non-empty string"},
		"Duplicate": {
			body:    `spectre.ingress<v1.Response>("http", "GET /v1/forecast"); spectre.ingress<v1.User>("http", "GET /v1/forecast");`,
			message: `duplicate endpoint "GET /v1/forecast"`,
		},
		"IngressWithHost": {
			body:    `spectre.ingress<v1.Response>("http", "GET weather.example/v1/forecast");`,
			message: `spectre.ingress pattern "GET weather.example/v1/forecast" must have the form "<METHOD> /<path>"`,
		},
		"IngressWithoutMethod": {
			body:    `spectre.ingress<v1.Response>("http", "/v1/forecast");`,
			message: `must have the form "<METHOD> /<path>"`,
		},
		"EgressNoArguments": {body: `egress();`, message: "spectre.egress requires a type name, a protocol, and a pattern"},
		"EgressWithoutHost": {
			body:    `spectre.egress<v1.Response>("http", "GET /v1/forecast");`,
			message: `spectre.egress pattern "GET /v1/forecast" must have the form "<METHOD> <host>/<path>"`,
		},
		"EgressWithoutMethod": {
			body:    `spectre.egress<v1.Response>("http", "weather.example/v1/forecast");`,
			message: `must have the form "<METHOD> <host>/<path>"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newProgram(t, untyped+test.body)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestEvaluatesEachScriptOnce(t *testing.T) {
	// Every file is loaded and users.ts is also imported twice; a second evaluation
	// would register a duplicate target.
	program, err := compile(t, fstest.MapFS{
		"weather.ts": script(`
			import "./lib/users";
			import "./lib/accounts/owners";
			spectre.field<v1.Response, "value">((value) => value);
		`),
		"lib/users.ts": script(`spectre.message<v1.User>((user) => user);`),
		"lib/accounts/owners.ts": script(`
			import "../users";
			spectre.field<v1.Response, "owner">((owner) => owner);
		`),
	})
	assert.NoError(t, err)

	assert.Equal(t, []javascript.FieldTarget{javascript.NewFieldTarget("test.v1.Response", "owner"), javascript.NewFieldTarget("test.v1.Response", "value")}, program.Fields())
	assert.Equal(t, []string{"test.v1.User"}, program.Messages())
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	normalised, present, err := evaluator.NormaliseMessage("test.v1.User", "alice", true)
	assert.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, any("alice"), normalised)
}

func TestRejectsDuplicateTargetsAcrossScripts(t *testing.T) {
	_, err := compile(t, fstest.MapFS{
		"weather.ts":   script(`spectre.message<v1.User>((user) => user);`),
		"lib/users.ts": script(`spectre.message<v1.User>((user) => user);`),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `duplicate normaliser target "test.v1.User"`)
}

func TestRejectsInvalidImports(t *testing.T) {
	library := &fstest.MapFile{Data: []byte(`export const value = 1;`)}
	for name, test := range map[string]struct {
		files   fstest.MapFS
		message string
	}{
		"Bare": {
			files:   fstest.MapFS{"weather.ts": script(`import "lib/users";`), "lib/users.ts": library},
			message: "Cannot find module or type declarations for side-effect import of 'lib/users'",
		},
		"SchemaModule": {
			files:   fstest.MapFS{"weather.ts": script(`import "test.v1";`)},
			message: `comparison module "weather.ts" must import modules by relative path, not "test.v1"`,
		},
		"SchemaFile": {
			files:   fstest.MapFS{"weather.ts": script(`import "../schema/test";`)},
			message: `comparison module "weather.ts" imports "../schema/test" outside the scripts directory`,
		},
		"Escapes": {
			files:   fstest.MapFS{"lib/users.ts": script(`import "../../users";`)},
			message: "Cannot find module or type declarations for side-effect import of '../../users'",
		},
		"UnsupportedExtension": {
			files:   fstest.MapFS{"weather.ts": script(`import "./data.json";`), "data.json": {Data: []byte(`{}`)}},
			message: "Cannot find module or type declarations for side-effect import of './data.json'",
		},
		"MissingSideEffect": {
			files:   fstest.MapFS{"weather.ts": script(`import "./lib/missing";`)},
			message: "Cannot find module or type declarations for side-effect import of './lib/missing'",
		},
		"MissingValue": {
			files:   fstest.MapFS{"weather.ts": script(`import { value } from "./lib/missing"; console.log(value);`)},
			message: "Cannot find module './lib/missing'",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(t, test.files)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestEvaluatorPreservesMissingAndNullArguments(t *testing.T) {
	program, err := newProgram(t, `
		spectre.field<v1.Response, "owner">((value: unknown) =>
			value === undefined ? "missing" : value === null ? "null" : "other");
	`)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	tests := map[string]struct {
		value    any
		present  bool
		expected any
	}{
		"Missing": {expected: "missing"},
		"Null":    {present: true, expected: "null"},
		"Value":   {value: "value", present: true, expected: "other"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			normalised, present, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "owner"), test.value, test.present)

			assert.NoError(t, err)
			assert.True(t, present)
			assert.Equal(t, test.expected, normalised)
		})
	}
}

func TestNormaliserResults(t *testing.T) {
	type result struct {
		Normalised any
		Present    bool
		Failed     bool
	}
	tests := map[string]struct {
		normaliser string
		expected   result
	}{
		"Sorted":                   {normaliser: `(value) => value.sort()`, expected: result{Normalised: []any{"a", "b"}, Present: true}},
		"Constant":                 {normaliser: `() => true`, expected: result{Normalised: true, Present: true}},
		"Null":                     {normaliser: `() => null`, expected: result{Present: true}},
		"UndefinedRemoves":         {normaliser: `() => undefined`},
		"UndefinedPropertyOmitted": {normaliser: `() => ({kept: 1, dropped: undefined})`, expected: result{Normalised: map[string]any{"kept": float64(1)}, Present: true}},
		"UndefinedElement":         {normaliser: `() => [undefined]`, expected: result{Failed: true}},
		"Function":                 {normaliser: `() => () => true`, expected: result{Failed: true}},
		"NestedFunction":           {normaliser: `() => ({nested: () => true})`, expected: result{Failed: true}},
		"NaN":                      {normaliser: `() => NaN`, expected: result{Failed: true}},
		"BigInt":                   {normaliser: `() => 1n`, expected: result{Failed: true}},
		"Throws":                   {normaliser: `() => { throw new Error("failed"); }`, expected: result{Failed: true}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// The cast lets results the declared type forbids reach the runtime checks.
			program, err := newProgram(t, `
				spectre.field<v1.Response, "values">((`+test.normaliser+`) as (value: string[]) => any);
			`)
			assert.NoError(t, err)
			evaluator, err := program.NewEvaluator(t.Context())
			assert.NoError(t, err)
			defer evaluator.Close()

			normalised, present, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "values"), []any{"b", "a"}, true)

			assert.Equal(t, test.expected, result{Normalised: normalised, Present: present, Failed: err != nil})
		})
	}
}

func TestNormalisersCannotReplaceJSONHelpers(t *testing.T) {
	program, err := newProgram(t, `
		JSON.stringify = () => "\"replaced\"";
		JSON.parse = () => "replaced";
		spectre.field<v1.Response, "value">((value) => value);
	`)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()

	normalised, present, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), "original", true)

	assert.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, any("original"), normalised)
}

func TestRejectsInvalidNormaliserDeclarations(t *testing.T) {
	tests := map[string]string{
		"FieldMissing":       `field();`,
		"MessageOne":         `message("test.v1.Response");`,
		"FieldTwo":           `field("test.v1.Response.value", () => true);`,
		"FieldFour":          `field("test.v1.Response", "value", () => true, "extra");`,
		"MessageThree":       `message("test.v1.Response", () => true, "extra");`,
		"FieldEmptyType":     `field("", "value", () => true);`,
		"FieldNonStringType": `field({}, "value", () => true);`,
		"FieldEmptyPath":     `field("test.v1.Response", "", () => true);`,
		"FieldNonStringPath": `field("test.v1.Response", 1, () => true);`,
		"FieldNonFunction":   `field("test.v1.Response", "value", true);`,
		"MessageEmptyType":   `message("", () => true);`,
		"MessageNonFunction": `message("test.v1.Response", true);`,
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newProgram(t, untyped+call)
			assert.Error(t, err)
		})
	}
}

func TestEvaluatorProtectsArgumentsFromMutation(t *testing.T) {
	program, err := newProgram(t, `
		spectre.message<v1.Response>((value) => {
			value!.value = "changed";
			return value;
		});
	`)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	value := map[string]any{"value": "original"}

	normalised, present, err := evaluator.NormaliseMessage("test.v1.Response", value, true)

	assert.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, any(map[string]any{"value": "changed"}), normalised)
	assert.Equal(t, map[string]any{"value": "original"}, value)
}

func TestEvaluatorsKeepConcurrentRuntimesIsolated(t *testing.T) {
	program, err := newProgram(t, `
		let calls = 0;
		spectre.field<v1.Response, "value">((value) => value + ":" + (++calls));
	`)
	assert.NoError(t, err)

	const evaluatorCount = 8
	type result struct {
		normalised any
		err        error
	}
	results := make(chan result, evaluatorCount)
	var wait sync.WaitGroup
	for range evaluatorCount {
		wait.Go(func() {
			evaluator, err := program.NewEvaluator(t.Context())
			if err != nil {
				results <- result{err: err}
				return
			}
			defer evaluator.Close()
			normalised, _, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), "same", true)
			results <- result{normalised: normalised, err: err}
		})
	}
	wait.Wait()
	close(results)

	for result := range results {
		assert.NoError(t, result.err)
		assert.Equal(t, any("same:1"), result.normalised)
	}
}

func TestTranspilesTypeScriptModules(t *testing.T) {
	program, err := compile(t, fstest.MapFS{
		"main.ts": script(`
			import { prefix, suffix } from "./lib/helpers";
			import type { Greeting } from "./lib/types";
			const greet = (value: Greeting): Greeting => prefix + value + suffix;
			spectre.ingress<v1.Response>("http", "GET /v1/forecast");
			spectre.field<v1.Response, "value">(greet);
		`),
		"lib/helpers.ts": {Data: []byte(`export const prefix: string = "hello "; export const suffix: string = "!";`)},
		"lib/types.d.ts": {Data: []byte(`export type Greeting = string;`)},
	})
	assert.NoError(t, err)
	assert.Equal(t, []javascript.FieldTarget{javascript.NewFieldTarget("test.v1.Response", "value")}, program.Fields())
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	normalised, present, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), "world", true)
	assert.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, any("hello world!"), normalised)
}

func TestEvaluatorsUseStartupModuleGraph(t *testing.T) {
	directory := t.TempDir()
	mainPath := filepath.Join(directory, "main.ts")
	helperPath := filepath.Join(directory, "helper.ts")
	assert.NoError(t, os.WriteFile(mainPath, script(`
		import { normalise } from "./helper";
		spectre.field<v1.Response, "value">(normalise);
	`).Data, 0o600))
	assert.NoError(t, os.WriteFile(helperPath, []byte(`export const normalise = (value: string) => value + "!";`), 0o600))
	program, err := javascript.NewProgram(t.Context(), declarations(), os.DirFS(directory))
	assert.NoError(t, err)
	assert.NoError(t, os.Remove(mainPath))
	assert.NoError(t, os.WriteFile(helperPath, []byte(`throw new Error("new source");`), 0o600))
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	value, present, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), "original", true)
	assert.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, any("original!"), value)
}

func TestExplicitTypeScriptImportsShareModuleRecord(t *testing.T) {
	program, err := compile(t, fstest.MapFS{
		"main.ts": script(`
			import "./lib/register";
			import "./lib/register.ts";
		`),
		"lib/register.ts": script(`spectre.message<v1.User>((user) => user);`),
	})
	assert.NoError(t, err)
	assert.Equal(t, []string{"test.v1.User"}, program.Messages())
}

func TestRejectsMalformedTypeScript(t *testing.T) {
	_, err := compile(t, fstest.MapFS{"main.ts": script(`const value: = 1;`)})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "scripts/main.ts(1,")
}

func TestRejectsScriptSymlinks(t *testing.T) {
	for _, name := range []string{"DirectFile", "ImportedDirectory"} {
		t.Run(name, func(t *testing.T) {
			scripts := t.TempDir()
			outside := t.TempDir()
			assert.NoError(t, os.WriteFile(filepath.Join(outside, "outside.ts"), []byte(`export const value = 1;`), 0o600))
			if name == "DirectFile" {
				assert.NoError(t, os.Symlink(filepath.Join(outside, "outside.ts"), filepath.Join(scripts, "link.ts")))
			} else {
				assert.NoError(t, os.Symlink(outside, filepath.Join(scripts, "lib")))
				assert.NoError(t, os.WriteFile(filepath.Join(scripts, "main.ts"), []byte(`import "./lib/outside";`), 0o600))
			}
			_, err := javascript.NewProgram(t.Context(), declarations(), os.DirFS(scripts))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "symbolic link")
		})
	}
}

func TestProtocolsAreNotValidatedUntilConfigure(t *testing.T) {
	program, err := newProgram(t, untyped+`ingress("test.v1.Response", "sql", "query");`)
	assert.NoError(t, err)
	assert.Equal(t, []endpoint{{"sql", "query", "test.v1.Response"}}, endpoints(program, javascript.Ingress))
}

func TestRejectsDuplicateFieldsAcrossScripts(t *testing.T) {
	_, err := compile(t, fstest.MapFS{
		"first.ts":  script(`spectre.field<v1.Response, "value">((value) => value);`),
		"second.ts": script(`spectre.field<v1.Response, "value">((value: string) => value);`),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `duplicate normaliser target "test.v1.Response.value"`)
}

func TestFieldRegistrationsPreserveExplicitTypeAndPath(t *testing.T) {
	program, err := newProgram(t, `
		import type * as test from "test";
		spectre.field<test.A, "B.value">(() => "first");
		spectre.field<test.A.B, "value">(() => "second");
	`)
	assert.NoError(t, err)
	first := javascript.NewFieldTarget("test.A", "B.value")
	second := javascript.NewFieldTarget("test.A.B", "value")
	assert.Equal(t, []javascript.FieldTarget{first, second}, program.Fields())
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	for name, test := range map[string]struct {
		target   javascript.FieldTarget
		expected string
	}{
		"First":  {target: first, expected: "first"},
		"Second": {target: second, expected: "second"},
	} {
		t.Run(name, func(t *testing.T) {
			value, present, err := evaluator.NormaliseField(test.target, "original", true)
			assert.NoError(t, err)
			assert.True(t, present)
			assert.Equal(t, any(test.expected), value)
		})
	}
}

type endpoint struct {
	protocol string
	pattern  string
	typeName string
}

func endpoints(program *javascript.Program, direction javascript.Direction) []endpoint {
	declared := []endpoint{}
	for _, value := range program.Endpoints(direction) {
		declared = append(declared, endpoint{value.Protocol(), value.Pattern(), value.Type()})
	}
	return declared
}

// untyped bypasses the checker so tests can reach the runtime's own argument checks.
const untyped = `
	const { ingress, egress, field, message } = spectre as unknown as Record<string, (...args: unknown[]) => void>;
`

// script is a module with the spectre module and the test.v1 types imported.
func script(body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(`import * as spectre from "spectre"; import type * as v1 from "test.v1";` + body)}
}

func newProgram(t *testing.T, body string) (*javascript.Program, error) {
	t.Helper()
	return compile(t, fstest.MapFS{"test.ts": script(body)})
}

func compile(t *testing.T, files fstest.MapFS) (*javascript.Program, error) {
	t.Helper()
	return javascript.NewProgram(t.Context(), declarations(), files)
}

func declarations() fstest.MapFS {
	return fstest.MapFS{"test.d.ts": {Data: []byte(testDeclarations)}}
}

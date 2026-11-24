package javascript_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/comparison/javascript/javascripttest"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
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
			spectre.egress.fake("GET weather.example/v1/forecast").match<v1.Response>();
			spectre.field<v1.Response, "value">(() => "fixed");
		`),
		"users/user.ts": script(`spectre.message<v1.User>((user) => user);`),
	})
	assert.NoError(t, err)

	assert.Equal(t, []endpoint{{"fake", "GET weather.example/v1/forecast", "test.v1.Response"}}, endpoints(program, model.Egress))
	assert.Equal(t, []javascript.FieldTarget{javascript.NewFieldTarget("test.v1.Response", "value")}, program.Fields())
	assert.Equal(t, []javascript.MessageTarget{javascript.NewMessageTarget("test.v1.User", "test.v1.User", true)}, program.Messages())
	assert.Equal(t, []schema.Method{{Interface: "test.v1.UserService", Name: "GetUser", Parameter: "test.v1.User", Result: "test.v1.User"}}, program.Schema().Methods())
}

func TestDeclaresNormalisedTypes(t *testing.T) {
	program, err := compile(t, fstest.MapFS{
		"users.ts": script(`
			export interface NormalisedUser { name?: string }
			spectre.message<v1.User, NormalisedUser>();
			spectre.field<NormalisedUser, "name">(() => undefined);
		`),
	})
	assert.NoError(t, err)
	assert.Equal(t, []javascript.MessageTarget{javascript.NewMessageTarget("test.v1.User", "NormalisedUser", false)}, program.Messages())
	assert.Equal(t, []javascript.FieldTarget{javascript.NewFieldTarget("NormalisedUser", "name")}, program.Fields())
	normalised, err := program.Schema().Type("NormalisedUser")
	assert.NoError(t, err)
	assert.Equal(t, &schema.ObjectType{Name: "NormalisedUser", Fields: []schema.Field{{Name: "name", Optional: true, Type: schema.StringType{}}}}, normalised)
}

func TestFieldNormaliserCannotRemoveRequiredField(t *testing.T) {
	_, err := newProgram(t, `spectre.field<v1.User, "name">(() => undefined);`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Type 'undefined' is not assignable to type 'string'")
}

func TestDeclaresOptionalEgressEndpoints(t *testing.T) {
	program, err := compile(t, fstest.MapFS{
		"weather.ts": script(`
			spectre.egress.fake("GET weather.example/v1/forecast").match<v1.Response>();
			spectre.egress.fake("GET weather.example/v1/users").optional().match<v1.User>();
			spectre.egress.fake("GET weather.example/v1/user").optional().replaces<v1.User, v1.User>("GET weather.example/v1/users").using<v1.User, v1.User>(
				(request) => request,
				(_request, reference) => reference,
			);
		`),
	})
	assert.NoError(t, err)

	optional := map[string]bool{}
	for _, declared := range program.Endpoints(model.Egress) {
		optional[declared.Pattern()] = declared.Optional()
	}
	assert.Equal(t, map[string]bool{
		"GET weather.example/v1/forecast": false,
		"GET weather.example/v1/users":    true,
		"GET weather.example/v1/user":     true,
	}, optional)
}

func TestLoadsEmptyScriptsDirectory(t *testing.T) {
	program, err := compile(t, fstest.MapFS{"README.md": {Data: []byte("Not a script.")}})
	assert.NoError(t, err)

	assert.Equal(t, []endpoint{}, endpoints(program, model.Egress))
	assert.Equal(t, []javascript.FieldTarget{}, program.Fields())
	assert.Equal(t, 0, len(program.Messages()))
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
	assert.Equal(t, []javascript.MessageTarget{javascript.NewMessageTarget("test.v1.User", "test.v1.User", true)}, program.Messages())
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
		"TextMessage": {
			files:   fstest.MapFS{"test.ts": script(`spectre.message<spectre.Text>((text) => text);`)},
			message: "only ingress rules may compare bodies as Text",
		},
		"TextEgress": {
			files:   fstest.MapFS{"test.ts": script(`spectre.egress.fake("GET weather.example/v1/forecast").match<spectre.Text>();`)},
			message: "only ingress rules may compare bodies as Text",
		},
		"UnknownMessage": {
			files:   fstest.MapFS{"test.ts": script(`spectre.message<unknown>((value) => value);`)},
			message: "error TS2344",
		},
		"UnknownEgress": {
			files:   fstest.MapFS{"test.ts": script(`spectre.egress.fake("GET weather.example/v1/forecast").match<unknown>();`)},
			message: "error TS2344",
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
		"NestedLocalType": {
			files:   fstest.MapFS{"test.ts": script(`function register() { interface Local { name: string } spectre.message<Local>((value) => value); }`)},
			message: `spectre.message type argument Local: type "Local" is not exported from a schema module or declared at the top level of a script`,
		},
		"DuplicateScriptTypeName": {
			files: fstest.MapFS{
				"a.ts": script(`export interface Same { value: string } spectre.egress.fake("GET a.example/x").match<Same>();`),
				"b.ts": script(`interface Same { value: string } spectre.egress.fake("GET b.example/y").match<Same>();`),
			},
			message: `types declared at scripts/a.ts:1:`,
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
			message: `object types may only declare properties; test.v1.UserService mixes them with methods`,
		},
		"Augmentation": {
			files: fstest.MapFS{"test.ts": script(`
				declare module "test.v1" { interface Extra { name: string } }
				spectre.message<v1.Extra>((value) => value);
			`)},
			message: `scripts/test.ts:2:32: Extra is declared in module "test.v1", but only schema files may declare modules`,
		},
		"AugmentedField": {
			files: fstest.MapFS{"test.ts": script(`
				declare module "test.v1" { interface Response { extra: string } }
				spectre.field<v1.Response, "extra">((value) => value);
			`)},
			message: "Response augments a schema module",
		},
		"AugmentedMethod": {
			files: fstest.MapFS{"test.ts": script(`
				declare module "test.v1" { interface UserService { Find(request: User): User } }
			`)},
			message: "UserService augments a schema module",
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
	assert.Equal(t, []javascript.MessageTarget{javascript.NewMessageTarget("test.v1.User", "test.v1.User", true)}, program.Messages())
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	normalised, err := evaluator.NormaliseMessage("test.v1.User", Some(unknownValue(t, `"alice"`)))
	assert.NoError(t, err)
	assert.Equal(t, Some(unknownValue(t, `"alice"`)), normalised)
}

func TestReadsVariables(t *testing.T) {
	files := fstest.MapFS{"test.ts": script(`
		spectre.egress.fake(` + "`GET forecasts.${spectre.variable(\"environment\")}.example/v1/forecast`" + `).match<v1.Response>();
		spectre.message<v1.User>(() => ({ name: spectre.variable("environment") }));
	`)}
	program, err := javascript.NewProgram(t.Context(), time.Second, declarations(), files, map[string]string{"environment": "staging"}, bindings())
	assert.NoError(t, err)

	assert.Equal(t, []endpoint{{"fake", "GET forecasts.staging.example/v1/forecast", "test.v1.Response"}}, endpoints(program, model.Egress))
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	normalised, err := evaluator.NormaliseMessage("test.v1.User", Some(unknownValue(t, `{"name":"alice"}`)))
	assert.NoError(t, err)
	assert.Equal(t, Some(unknownValue(t, `{"name":"staging"}`)), normalised)
}

func TestRejectsUnsetVariable(t *testing.T) {
	_, err := newProgram(t, `spectre.egress.fake(`+"`GET forecasts.${spectre.variable(\"environment\")}.example/v1/forecast`"+`).match<v1.Response>();`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `spectre.variable "environment" is not set`)
}

func TestTimeoutBoundsOnlyEvaluation(t *testing.T) {
	_, err := javascript.NewProgram(t.Context(), 10*time.Millisecond, declarations(), fstest.MapFS{"test.ts": script(`while (true) {}`)}, nil, bindings())
	assert.IsError(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "evaluate comparison modules")
	assert.Contains(t, err.Error(), "module evaluation")
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
		value    Option[schema.Value]
		expected string
	}{
		"Missing": {value: None[schema.Value](), expected: "missing"},
		"Null":    {value: Some(unknownValue(t, `null`)), expected: "null"},
		"Value":   {value: Some(unknownValue(t, `"value"`)), expected: "other"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			normalised, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "owner"), test.value)

			assert.NoError(t, err)
			assert.Equal(t, Some(schema.NewString(test.expected)), normalised)
		})
	}
}

func TestNormaliserResults(t *testing.T) {
	type result struct {
		Normalised Option[schema.Value]
		Failed     bool
	}
	tests := map[string]struct {
		normaliser string
		expected   result
	}{
		"Sorted":                   {normaliser: `(value) => value.sort()`, expected: result{Normalised: Some(unknownValue(t, `["a","b"]`))}},
		"Constant":                 {normaliser: `() => true`, expected: result{Normalised: Some(unknownValue(t, `true`))}},
		"Null":                     {normaliser: `() => null`, expected: result{Normalised: Some(unknownValue(t, `null`))}},
		"UndefinedRemoves":         {normaliser: `() => undefined`},
		"UndefinedPropertyOmitted": {normaliser: `() => ({kept: 1, dropped: undefined})`, expected: result{Normalised: Some(unknownValue(t, `{"kept":1}`))}},
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

			normalised, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "values"), Some(unknownValue(t, `["b","a"]`)))

			assert.Equal(t, test.expected, result{Normalised: normalised, Failed: err != nil})
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

	normalised, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), Some(unknownValue(t, `"original"`)))

	assert.NoError(t, err)
	assert.Equal(t, Some(unknownValue(t, `"original"`)), normalised)
}

func TestRejectsInvalidNormaliserDeclarations(t *testing.T) {
	tests := map[string]string{
		"FieldMissing":           `field();`,
		"MessageEmptyNormalised": `message("test.v1.Response", "", () => true);`,
		"FieldTwo":               `field("test.v1.Response.value", () => true);`,
		"FieldFour":              `field("test.v1.Response", "value", () => true, "extra");`,
		"MessageThree":           `message("test.v1.Response", () => true, "extra");`,
		"FieldEmptyType":         `field("", "value", () => true);`,
		"FieldNonStringType":     `field({}, "value", () => true);`,
		"FieldEmptyPath":         `field("test.v1.Response", "", () => true);`,
		"FieldNonStringPath":     `field("test.v1.Response", 1, () => true);`,
		"FieldNonFunction":       `field("test.v1.Response", "value", true);`,
		"MessageEmptyType":       `message("", () => true);`,
		"MessageNonFunction":     `message("test.v1.Response", true);`,
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
	value := unknownValue(t, `{"value":"original"}`)

	normalised, err := evaluator.NormaliseMessage("test.v1.Response", Some(value))

	assert.NoError(t, err)
	assert.Equal(t, Some(unknownValue(t, `{"value":"changed"}`)), normalised)
	assert.Equal(t, unknownValue(t, `{"value":"original"}`), value)
}

func TestEvaluatorsKeepConcurrentRuntimesIsolated(t *testing.T) {
	program, err := newProgram(t, `
		let calls = 0;
		spectre.field<v1.Response, "value">((value) => value + ":" + (++calls));
	`)
	assert.NoError(t, err)

	const evaluatorCount = 8
	type result struct {
		normalised Option[schema.Value]
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
			normalised, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), Some(unknownValue(t, `"same"`)))
			results <- result{normalised: normalised, err: err}
		})
	}
	wait.Wait()
	close(results)

	for result := range results {
		assert.NoError(t, result.err)
		assert.Equal(t, Some(unknownValue(t, `"same:1"`)), result.normalised)
	}
}

func TestTranspilesTypeScriptModules(t *testing.T) {
	program, err := compile(t, fstest.MapFS{
		"main.ts": script(`
			import { prefix, suffix } from "./lib/helpers";
			import type { Greeting } from "./lib/types";
			const greet = (value: Greeting): Greeting => prefix + value + suffix;
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
	normalised, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), Some(unknownValue(t, `"world"`)))
	assert.NoError(t, err)
	assert.Equal(t, Some(unknownValue(t, `"hello world!"`)), normalised)
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
	program, err := javascript.NewProgram(t.Context(), time.Second, declarations(), os.DirFS(directory), nil, bindings())
	assert.NoError(t, err)
	assert.NoError(t, os.Remove(mainPath))
	assert.NoError(t, os.WriteFile(helperPath, []byte(`throw new Error("new source");`), 0o600))
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	value, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), Some(unknownValue(t, `"original"`)))
	assert.NoError(t, err)
	assert.Equal(t, Some(unknownValue(t, `"original!"`)), value)
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
	assert.Equal(t, []javascript.MessageTarget{javascript.NewMessageTarget("test.v1.User", "test.v1.User", true)}, program.Messages())
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
			_, err := javascript.NewProgram(t.Context(), time.Second, declarations(), os.DirFS(scripts), nil, bindings())
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "symbolic link")
		})
	}
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
			value, err := evaluator.NormaliseField(test.target, Some(unknownValue(t, `"original"`)))
			assert.NoError(t, err)
			assert.Equal(t, Some(unknownValue(t, `"`+test.expected+`"`)), value)
		})
	}
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

// untyped bypasses the checker so tests can reach the runtime's own argument checks.
const untyped = `
	const { ingress, egress } = spectre as unknown as Record<string, Record<string, (...args: unknown[]) => any>>;
	const { field, message } = spectre as unknown as Record<string, (...args: unknown[]) => void>;
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
	return javascript.NewProgram(t.Context(), time.Second, declarations(), files, nil, bindings())
}

// bindings are the script bindings of the fake protocol, which declares endpoints and replacements.
func bindings() []javascript.Binding {
	return []javascript.Binding{javascripttest.Binding()}
}

func declarations() fstest.MapFS {
	return fstest.MapFS{"test.d.ts": {Data: []byte(testDeclarations)}}
}

// unknownValue decodes JSON as a value of type unknown, which is what normalisers return.
func unknownValue(t *testing.T, data string) schema.Value {
	t.Helper()
	value, err := schema.Decode(schema.UnknownType{}, []byte(data))
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
	value, err := schema.Decode(objectType(t, program, name), []byte(data))
	assert.NoError(t, err)
	return value
}

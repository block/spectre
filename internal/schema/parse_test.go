package schema_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/schema"
)

func TestParsesDeclarations(t *testing.T) {
	loaded, err := schema.Parse(t.Context(), fstest.MapFS{
		"users.d.ts": {Data: []byte(`
declare module "example.users.v1" {
  import type { Audit } from "audit";
  export type Role = "ROLE_ADMIN" | "ROLE_READER";
  export interface User extends Audit {
    id: string;
    age?: number;
    ratio: number | "NaN" | "Infinity";
    active: boolean;
    roles: Role[];
    tags: ("a" | "b")[];
    labels: Record<string, string>;
    scores: { [key: string]: number };
    profile?: User.Profile;
    "@type"?: string;
  }
  export namespace User {
    export interface Profile { team: string }
  }
  export interface UserService {
    GetUser(request: GetUserRequest): import("example.users.v1").User;
  }
}
`)},
		"nested/requests.d.ts": {Data: []byte(`
declare module "example.users.v1" {
  export type GetUserRequest = { id: string };
}
declare module "audit" {
  export interface Audit { updatedBy?: string }
}
`)},
		"ignored.js": {Data: []byte(`not TypeScript`)},
	})
	assert.NoError(t, err)
	role := schema.Value{Kind: schema.KindEnum, Literals: []string{"ROLE_ADMIN", "ROLE_READER"}, Type: "example.users.v1.Role"}
	assert.Equal(t, []*schema.Type{
		{Name: "audit.Audit", Fields: []schema.Field{
			{Name: "updatedBy", Optional: true, Value: schema.Value{Kind: schema.KindString}},
		}},
		{Name: "example.users.v1.GetUserRequest", Fields: []schema.Field{
			{Name: "id", Value: schema.Value{Kind: schema.KindString}},
		}},
		{Name: "example.users.v1.User", Fields: []schema.Field{
			{Name: "updatedBy", Optional: true, Value: schema.Value{Kind: schema.KindString}},
			{Name: "id", Value: schema.Value{Kind: schema.KindString}},
			{Name: "age", Optional: true, Value: schema.Value{Kind: schema.KindNumber}},
			{Name: "ratio", Value: schema.Value{Kind: schema.KindNumber, Literals: []string{"Infinity", "NaN"}}},
			{Name: "active", Value: schema.Value{Kind: schema.KindBoolean}},
			{Name: "roles", Value: schema.Value{Kind: schema.KindList, Element: Some(&role)}},
			{Name: "tags", Value: schema.Value{Kind: schema.KindList, Element: Some(&schema.Value{Kind: schema.KindEnum, Literals: []string{"a", "b"}})}},
			{Name: "labels", Value: schema.Value{Kind: schema.KindMap, Element: Some(&schema.Value{Kind: schema.KindString})}},
			{Name: "scores", Value: schema.Value{Kind: schema.KindMap, Element: Some(&schema.Value{Kind: schema.KindNumber})}},
			{Name: "profile", Optional: true, Value: schema.Value{Kind: schema.KindObject, Type: "example.users.v1.User.Profile"}},
			{Name: "@type", Optional: true, Value: schema.Value{Kind: schema.KindString}},
		}},
		{Name: "example.users.v1.User.Profile", Fields: []schema.Field{
			{Name: "team", Value: schema.Value{Kind: schema.KindString}},
		}},
	}, loaded.Types())
	assert.Equal(t, []schema.Operation{{
		Name:     "example.users.v1.UserService.GetUser",
		Request:  "example.users.v1.GetUserRequest",
		Response: "example.users.v1.User",
	}}, loaded.Operations())
}

func TestRejectsSchemaSymlinks(t *testing.T) {
	for name, test := range map[string]struct {
		link      string
		directory bool
	}{
		"File":            {link: "link.ts"},
		"DeclarationFile": {link: "link.d.ts"},
		"Directory":       {link: "nested", directory: true},
		"IgnoredFile":     {link: "link.js"},
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			outside := t.TempDir()
			target := filepath.Join(outside, "outside.ts")
			assert.NoError(t, os.WriteFile(target, []byte(`not valid TypeScript`), 0o600))
			if test.directory {
				target = outside
			}
			assert.NoError(t, os.Symlink(target, filepath.Join(directory, test.link)))
			_, err := schema.Parse(t.Context(), os.DirFS(directory))
			assert.EqualError(t, err, `schema path "`+test.link+`" is a symbolic link`)
		})
	}
}

func TestSameNameInDifferentModules(t *testing.T) {
	loaded, err := schema.ParseSources(t.Context(), map[string]string{
		"a.d.ts": `declare module "a" { export interface Response { a: string } }`,
		"b.d.ts": `declare module "b" { export interface Response { b: number } }`,
	})
	assert.NoError(t, err)
	assert.Equal(t, []*schema.Type{
		{Name: "a.Response", Fields: []schema.Field{{Name: "a", Value: schema.Value{Kind: schema.KindString}}}},
		{Name: "b.Response", Fields: []schema.Field{{Name: "b", Value: schema.Value{Kind: schema.KindNumber}}}},
	}, loaded.Types())
}

func TestRejectsDeclarations(t *testing.T) {
	for name, test := range map[string]struct {
		source string
		error  string
	}{
		"SyntaxError":       {source: `declare module "m" { interface A {`, error: "schema/a.d.ts(1,35): error TS1005: '}' expected."},
		"TypeError":         {source: `declare module "m" { interface A { b: B } }`, error: "Cannot find name 'B'."},
		"Global":            {source: "interface A {}", error: `schema/a.d.ts:1:1: schema files may only contain declare module "name" { ... } blocks`},
		"Namespace":         {source: "declare namespace a { interface A {} }", error: "schema files may only contain declare module"},
		"TopLevelExport":    {source: `export interface A {}`, error: "schema files may only contain declare module"},
		"Reserved":          {source: `declare module "spectre" {}`, error: `module "spectre" is reserved for the script API`},
		"Class":             {source: `declare module "m" { class A {} }`, error: "schema/a.d.ts:1:22: schema modules may only declare interfaces, type aliases, and namespaces"},
		"Enum":              {source: `declare module "m" { enum A { B } }`, error: "schema modules may only declare interfaces"},
		"Function":          {source: `declare module "m" { function f(): void }`, error: "schema modules may only declare interfaces"},
		"ReExport":          {source: `declare module "m" { export { A } from "n" }` + "\n" + `declare module "n" { interface A {} }`, error: "schema modules may only declare interfaces"},
		"Generic":           {source: `declare module "m" { interface A<T> { a: T } }`, error: "generic declarations are not supported"},
		"GenericAlias":      {source: `declare module "m" { type A<T> = { a: T } }`, error: "generic declarations are not supported"},
		"Any":               {source: `declare module "m" { interface A { a: any } }`, error: "schema/a.d.ts:1:36: type any is not supported in schemas"},
		"Unknown":           {source: `declare module "m" { interface A { a: unknown } }`, error: "type unknown is not supported"},
		"Null":              {source: `declare module "m" { interface A { a: string | null } }`, error: "type string | null is not supported"},
		"RequiredUndefined": {source: `declare module "m" { interface A { a: string | undefined } }`, error: "type string | undefined is not supported"},
		"NumberLiteral":     {source: `declare module "m" { interface A { a: 1 } }`, error: "type 1 is not supported"},
		"MixedUnion":        {source: `declare module "m" { interface A { a: string | number } }`, error: `type string | number is not supported`},
		"Intersection":      {source: `declare module "m" { interface B { b: string } interface C { c: string } interface A { a: B & C } }`, error: "type B & C is not supported"},
		"FunctionType":      {source: `declare module "m" { interface A { a: () => void } }`, error: "type () => void is not supported"},
		"Tuple":             {source: `declare module "m" { interface A { a: [string] } }`, error: "type [string] is not supported"},
		"InlineObject":      {source: `declare module "m" { interface A { a: { b: string } } }`, error: "object types must be declared by name in a schema module"},
		"LibraryObject":     {source: `declare module "m" { interface A { a: Date } }`, error: `type "Date" is not exported from a schema module`},
		"IndexAndProperty":  {source: `declare module "m" { interface A { a: { [key: string]: string; b: string } } }`, error: "maps must have the form"},
		"NumberKeys":        {source: `declare module "m" { interface A { a: Record<number, string> } }`, error: "maps must have the form"},
		"GenericUse":        {source: `declare module "m" { type P<T> = { a: T }; interface A { a: P<string> } }`, error: "generic declarations are not supported"},
		"MixedService":      {source: `declare module "m" { interface B {} interface A { a: string; Get(request: B): B } }`, error: "service interfaces may only declare methods"},
		"ServiceParameters": {source: `declare module "m" { interface B {} interface A { Get(a: B, b: B): B } }`, error: "service methods must have the form Get(request: Request): Response"},
		"OptionalParameter": {source: `declare module "m" { interface B {} interface A { Get(a?: B): B } }`, error: "must have the form"},
		"GenericMethod":     {source: `declare module "m" { interface B {} interface A { Get<T>(a: B): B } }`, error: "must have the form"},
		"Overloads":         {source: `declare module "m" { interface B {} interface A { Get(a: B): B; Get(a: A): B } }`, error: "must have the form"},
		"ServiceScalar":     {source: `declare module "m" { interface B {} interface A { Get(request: string): B } }`, error: "expected an object type, found string"},
		"ServiceAsType":     {source: `declare module "m" { interface B {} interface S { Get(request: B): B } interface A { s: S } }`, error: "object types may only declare properties; m.S mixes them with methods"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := schema.ParseSources(t.Context(), map[string]string{"a.d.ts": test.source})
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.error)
		})
	}
}

func TestModulesDoNotShareScope(t *testing.T) {
	_, err := schema.ParseSources(t.Context(), map[string]string{
		"a.d.ts": `declare module "m" { interface A { b: B } }` + "\n" + `declare module "n" { export interface B {} }`,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Cannot find name 'B'")
}

func TestCancelledCheckReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := schema.ParseSources(ctx, map[string]string{
		"a.d.ts": `declare module "a" { export interface A {} }`,
		"b.d.ts": `declare module "b" { export interface B {} }`,
	})
	assert.IsError(t, err, context.Canceled)
}

func TestValidatesJSON(t *testing.T) {
	loaded, err := schema.ParseSources(t.Context(), map[string]string{"a.d.ts": `
declare module "m" {
  interface A { name: string; count?: number; ratio?: number | "NaN"; flag?: boolean; kind?: "x" | "y"; items?: B[]; byKey?: Record<string, B> }
  interface B { id: string }
}
`})
	assert.NoError(t, err)
	for name, test := range map[string]struct {
		value any
		error string
	}{
		"Minimal":         {value: map[string]any{"name": "a"}},
		"Complete":        {value: map[string]any{"name": "a", "count": 1.0, "flag": true, "kind": "x", "items": []any{map[string]any{"id": "b"}}, "byKey": map[string]any{"k": map[string]any{"id": "c"}}}},
		"NotObject":       {value: []any{}, error: "$: expected m.A, found an array"},
		"MissingRequired": {value: map[string]any{}, error: `$: required field "name" of type "m.A" is missing`},
		"UnknownField":    {value: map[string]any{"name": "a", "other": 1.0}, error: `$: type "m.A" has no field "other"`},
		"Null":            {value: map[string]any{"name": "a", "count": nil}, error: "$.count: expected number, found null"},
		"WrongScalar":     {value: map[string]any{"name": 1.0}, error: "$.name: expected string, found a number"},
		"WrongLiteral":    {value: map[string]any{"name": "a", "kind": "z"}, error: `$.kind: value is not one of "x" | "y"`},
		"NumberLiteral":   {value: map[string]any{"name": "a", "ratio": "NaN"}},
		"NumberString":    {value: map[string]any{"name": "a", "ratio": "Infinity"}, error: `$.ratio: expected number | "NaN", found a string`},
		"NestedElement":   {value: map[string]any{"name": "a", "items": []any{map[string]any{}}}, error: `$.items[0]: required field "id" of type "m.B" is missing`},
		"NestedMapValue":  {value: map[string]any{"name": "a", "byKey": map[string]any{"k": "v"}}, error: `$.byKey["k"]: expected m.B, found a string`},
	} {
		t.Run(name, func(t *testing.T) {
			err := loaded.Validate("m.A", test.value)
			if test.error == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, test.error)
		})
	}
}

func TestRendersValues(t *testing.T) {
	text := schema.Value{Kind: schema.KindString}
	for expected, value := range map[string]schema.Value{
		"string":                  text,
		`("a" | "b")[]`:           {Kind: schema.KindList, Element: Some(&schema.Value{Kind: schema.KindEnum, Literals: []string{"a", "b"}})},
		"Role[]":                  {Kind: schema.KindList, Element: Some(&schema.Value{Kind: schema.KindEnum, Literals: []string{"a", "b"}, Type: "Role"})},
		"Record<string, a.B[]>":   {Kind: schema.KindMap, Element: Some(&schema.Value{Kind: schema.KindList, Element: Some(&schema.Value{Kind: schema.KindObject, Type: "a.B"})})},
		`Record<string, "a\"b">`:  {Kind: schema.KindMap, Element: Some(&schema.Value{Kind: schema.KindEnum, Literals: []string{`a"b`}})},
		"Record<string, boolean>": {Kind: schema.KindMap, Element: Some(&schema.Value{Kind: schema.KindBoolean})},
		`number | "NaN"`:          {Kind: schema.KindNumber, Literals: []string{"NaN"}},
		`(number | "NaN")[]`:      {Kind: schema.KindList, Element: Some(&schema.Value{Kind: schema.KindNumber, Literals: []string{"NaN"}})},
		"number[]":                {Kind: schema.KindList, Element: Some(&schema.Value{Kind: schema.KindNumber})},
	} {
		assert.Equal(t, expected, value.String())
	}
}

func TestRejectsCollectionsWithoutElement(t *testing.T) {
	for _, kind := range []schema.Kind{schema.KindList, schema.KindMap} {
		t.Run(string(kind), func(t *testing.T) {
			_, err := schema.New([]*schema.Type{{Name: "m.A", Fields: []schema.Field{{Name: "a", Value: schema.Value{Kind: kind}}}}}, nil)
			assert.EqualError(t, err, `field "a" of type "m.A": `+string(kind)+" has no element type")
		})
	}
}

func TestValueEqualComparesElementPresence(t *testing.T) {
	text := schema.Value{Kind: schema.KindList, Element: Some(&schema.Value{Kind: schema.KindString})}
	assert.True(t, text.Equal(schema.Value{Kind: schema.KindList, Element: Some(&schema.Value{Kind: schema.KindString})}))
	assert.False(t, text.Equal(schema.Value{Kind: schema.KindList}))
	assert.True(t, schema.Value{Kind: schema.KindList}.Equal(schema.Value{Kind: schema.KindList}))
}

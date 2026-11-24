package schema_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	role := schema.EnumType{Name: Some(schema.TypeRef("example.users.v1.Role")), Literals: []string{"ROLE_ADMIN", "ROLE_READER"}}
	profile := &schema.ObjectType{Name: "example.users.v1.User.Profile", Fields: []schema.Field{
		{Name: "team", Type: schema.StringType{}},
	}}
	assert.Equal(t, []*schema.ObjectType{
		{Name: "audit.Audit", Fields: []schema.Field{
			{Name: "updatedBy", Optional: true, Type: schema.StringType{}},
		}},
		{Name: "example.users.v1.GetUserRequest", Fields: []schema.Field{
			{Name: "id", Type: schema.StringType{}},
		}},
		{Name: "example.users.v1.User", Fields: []schema.Field{
			{Name: "id", Type: schema.StringType{}},
			{Name: "age", Optional: true, Type: schema.NumberType{}},
			{Name: "ratio", Type: schema.NumberType{Literals: []string{"Infinity", "NaN"}}},
			{Name: "active", Type: schema.BooleanType{}},
			{Name: "roles", Type: schema.ListType{Element: role}},
			{Name: "tags", Type: schema.ListType{Element: schema.EnumType{Literals: []string{"a", "b"}}}},
			{Name: "labels", Type: schema.MapType{Element: schema.StringType{}}},
			{Name: "scores", Type: schema.MapType{Element: schema.NumberType{}}},
			{Name: "profile", Optional: true, Type: profile},
			{Name: "@type", Optional: true, Type: schema.StringType{}},
			{Name: "updatedBy", Optional: true, Type: schema.StringType{}},
		}},
		profile,
	}, schemaTypes(loaded))
	assert.Equal(t, []schema.Method{{
		Interface: "example.users.v1.UserService",
		Name:      "GetUser",
		Parameter: "example.users.v1.GetUserRequest",
		Result:    "example.users.v1.User",
	}}, loaded.Methods())
}

func TestParsesNullableMembers(t *testing.T) {
	loaded, err := schema.ParseSources(t.Context(), map[string]string{"a.d.ts": `
declare module "m" {
  export interface A {
    id: string | null;
    note?: string | null;
    null_first: null | number;
    flag: boolean | null;
    kind: "x" | "y" | null;
    child: B | null;
    items: B[] | null;
    byKey?: Record<string, string> | null;
    texts: (string | null)[];
    byName: Record<string, B | null>;
    anything: unknown;
    plain: string;
  }
  export interface B { id: string }
}
`})
	assert.NoError(t, err)
	child := &schema.ObjectType{Name: "m.B", Fields: []schema.Field{{Name: "id", Type: schema.StringType{}}}}
	nullableChild := schema.NullableType{Type: child}
	declared, err := loaded.Type("m.A")
	assert.NoError(t, err)
	assert.Equal(t, &schema.ObjectType{Name: "m.A", Fields: []schema.Field{
		{Name: "id", Type: schema.NullableType{Type: schema.StringType{}}},
		{Name: "note", Optional: true, Type: schema.NullableType{Type: schema.StringType{}}},
		{Name: "null_first", Type: schema.NullableType{Type: schema.NumberType{}}},
		{Name: "flag", Type: schema.NullableType{Type: schema.BooleanType{}}},
		{Name: "kind", Type: schema.NullableType{Type: schema.EnumType{Literals: []string{"x", "y"}}}},
		{Name: "child", Type: nullableChild},
		{Name: "items", Type: schema.NullableType{Type: schema.ListType{Element: child}}},
		{Name: "byKey", Optional: true, Type: schema.NullableType{Type: schema.MapType{Element: schema.StringType{}}}},
		{Name: "texts", Type: schema.ListType{Element: schema.NullableType{Type: schema.StringType{}}}},
		{Name: "byName", Type: schema.MapType{Element: nullableChild}},
		{Name: "anything", Type: schema.UnknownType{}},
		{Name: "plain", Type: schema.StringType{}},
	}}, declared)
}

func TestPayloadModules(t *testing.T) {
	const (
		connect = `declare module "spectre.fake" { interface Error { code: string } }`
		other   = `declare module "spectre.other" { interface Thing { name: string } }`
	)
	for name, test := range map[string]struct {
		payloads []string
		source   string
		error    string
	}{
		"Valid":             {payloads: []string{connect, other}, source: `declare module "m" { interface A { a: string } }`},
		"NoPayloads":        {source: `declare module "m" { interface A { a: string } }`},
		"ReservedByPayload": {payloads: []string{connect}, source: `declare module "spectre.fake" { interface A {} }`, error: `module "spectre.fake" is reserved by SPECTRE`},
		"UnreservedName":    {payloads: []string{connect}, source: `declare module "spectre.sample.v1" { interface A {} }`},
		"Duplicate":         {payloads: []string{connect, connect}, error: `payload module "spectre.fake" is declared more than once`},
		"OutsideNamespace":  {payloads: []string{`declare module "other" { interface A {} }`}, error: `payload module "other" must be named under "spectre."`},
		"SpectreItself":     {payloads: []string{`declare module "spectre" { interface A {} }`}, error: `payload module "spectre" must be named under "spectre."`},
	} {
		t.Run(name, func(t *testing.T) {
			sources := map[string]string{}
			if test.source != "" {
				sources["a.d.ts"] = test.source
			}
			loaded, err := schema.ParseSources(t.Context(), sources, test.payloads...)
			if test.error != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), test.error)
				return
			}
			assert.NoError(t, err)
			if len(test.payloads) > 0 {
				_, err := loaded.Type("spectre.fake.Error")
				assert.NoError(t, err)
			}
		})
	}
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
	assert.Equal(t, []*schema.ObjectType{
		{Name: "a.Response", Fields: []schema.Field{{Name: "a", Type: schema.StringType{}}}},
		{Name: "b.Response", Fields: []schema.Field{{Name: "b", Type: schema.NumberType{}}}},
	}, schemaTypes(loaded))
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
		"Reserved":          {source: `declare module "spectre" {}`, error: `module "spectre" is reserved by SPECTRE`},
		"Class":             {source: `declare module "m" { class A {} }`, error: "schema/a.d.ts:1:22: schema modules may only declare interfaces, type aliases, and namespaces"},
		"Enum":              {source: `declare module "m" { enum A { B } }`, error: "schema modules may only declare interfaces"},
		"Function":          {source: `declare module "m" { function f(): void }`, error: "schema modules may only declare interfaces"},
		"ReExport":          {source: `declare module "m" { export { A } from "n" }` + "\n" + `declare module "n" { interface A {} }`, error: "schema modules may only declare interfaces"},
		"Generic":           {source: `declare module "m" { interface A<T> { a: T } }`, error: "generic declarations are not supported"},
		"GenericAlias":      {source: `declare module "m" { type A<T> = { a: T } }`, error: "generic declarations are not supported"},
		"Any":               {source: `declare module "m" { interface A { a: any } }`, error: "schema/a.d.ts:1:36: type any is not supported in schemas"},
		"Null":              {source: `declare module "m" { interface A { a: null } }`, error: "type null is not supported"},
		"NullableService":   {source: `declare module "m" { interface B {} interface A { Get(request: B | null): B } }`, error: "expected an object type, found m.B | null"},
		"NullableMixed":     {source: `declare module "m" { interface A { a: string | number | null } }`, error: "type string | number is not supported"},
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
  interface A {
    name: string; nick: string | null; tag?: string | null; count?: number; ratio?: number | "NaN"; flag?: boolean;
    kind?: "x" | "y"; items?: B[]; byKey?: Record<string, B>; texts?: (string | null)[]; extra?: unknown
  }
  interface B { id: string }
}
`})
	assert.NoError(t, err)
	declared, err := loaded.Type("m.A")
	assert.NoError(t, err)
	for name, test := range map[string]struct {
		value any
		error string
	}{
		"Minimal":         {value: map[string]any{"name": "a", "nick": nil}},
		"NullableString":  {value: map[string]any{"name": "a", "nick": "n"}},
		"NullableValue":   {value: map[string]any{"name": "a", "nick": "n", "tag": nil}},
		"NullableWrong":   {value: map[string]any{"name": "a", "nick": 1.0}, error: "$.nick: expected string | null, found a number"},
		"NullableMissing": {value: map[string]any{"name": "a"}, error: `$: required field "nick" of type "m.A" is missing`},
		"RequiredNull":    {value: map[string]any{"name": nil, "nick": nil}, error: "$.name: expected string, found null"},
		"Complete":        {value: map[string]any{"name": "a", "nick": "n", "count": 1.0, "flag": true, "kind": "x", "items": []any{map[string]any{"id": "b"}}, "byKey": map[string]any{"k": map[string]any{"id": "c"}}}},
		"NotObject":       {value: []any{}, error: "$: expected m.A, found an array"},
		"MissingRequired": {value: map[string]any{}, error: `$: required field "name" of type "m.A" is missing`},
		"UnknownField":    {value: map[string]any{"name": "a", "nick": nil, "other": 1.0}, error: `$: type "m.A" has a field it does not declare`},
		"Null":            {value: map[string]any{"name": "a", "nick": nil, "count": nil}, error: "$.count: expected number, found null"},
		"WrongScalar":     {value: map[string]any{"name": 1.0}, error: "$.name: expected string, found a number"},
		"WrongLiteral":    {value: map[string]any{"name": "a", "nick": nil, "kind": "z"}, error: `$.kind: value is not one of "x" | "y"`},
		"NumberLiteral":   {value: map[string]any{"name": "a", "nick": nil, "ratio": "NaN"}},
		"NumberString":    {value: map[string]any{"name": "a", "nick": nil, "ratio": "Infinity"}, error: `$.ratio: expected number | "NaN", found a string`},
		"NestedElement":   {value: map[string]any{"name": "a", "nick": nil, "items": []any{map[string]any{}}}, error: `$.items[0]: required field "id" of type "m.B" is missing`},
		"NestedMapValue":  {value: map[string]any{"name": "a", "nick": nil, "byKey": map[string]any{"alice@example.com": "v"}}, error: `$.byKey[*]: expected m.B, found a string`},
		"NullElement":     {value: map[string]any{"name": "a", "nick": nil, "texts": []any{"t", nil}}},
		"WrongElement":    {value: map[string]any{"name": "a", "nick": nil, "texts": []any{true}}, error: "$.texts[0]: expected string | null, found a boolean"},
		"UnknownJSON":     {value: map[string]any{"name": "a", "nick": nil, "extra": map[string]any{"k": []any{1.0, nil, "s", false}}}},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(test.value)
			assert.NoError(t, err)
			_, err = schema.Decode(declared, data)
			if test.error == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, test.error)
		})
	}
}

func TestRendersTypes(t *testing.T) {
	object := &schema.ObjectType{Name: "a.B"}
	enum := schema.EnumType{Literals: []string{"a", "b"}}
	number := schema.NumberType{Literals: []string{"NaN"}}
	for expected, declared := range map[string]schema.Type{
		"string":                     schema.StringType{},
		`("a" | "b")[]`:              schema.ListType{Element: enum},
		"Role[]":                     schema.ListType{Element: schema.EnumType{Name: Some(schema.TypeRef("Role")), Literals: []string{"a", "b"}}},
		"Record<string, a.B[]>":      schema.MapType{Element: schema.ListType{Element: object}},
		`Record<string, "a\"b">`:     schema.MapType{Element: schema.EnumType{Literals: []string{`a"b`}}},
		"Record<string, boolean>":    schema.MapType{Element: schema.BooleanType{}},
		`number | "NaN"`:             number,
		`(number | "NaN")[]`:         schema.ListType{Element: number},
		"number[]":                   schema.ListType{Element: schema.NumberType{}},
		"unknown":                    schema.UnknownType{},
		"a.B | null":                 schema.NullableType{Type: object},
		"(string | null)[]":          schema.ListType{Element: schema.NullableType{Type: schema.StringType{}}},
		"Record<string, a.B | null>": schema.MapType{Element: schema.NullableType{Type: object}},
	} {
		assert.Equal(t, expected, declared.String())
	}
}

func TestRejectsInvalidTypes(t *testing.T) {
	for name, test := range map[string]struct {
		field schema.Type
		error string
	}{
		"ListWithoutElement":   {field: schema.ListType{}, error: `field "a" of type "m.A": type is missing`},
		"MapWithoutElement":    {field: schema.MapType{}, error: `field "a" of type "m.A": type is missing`},
		"EnumWithoutLiterals":  {field: schema.EnumType{}, error: `field "a" of type "m.A": enum has no literals`},
		"NestedNullable":       {field: schema.NullableType{Type: schema.NullableType{Type: schema.StringType{}}}, error: `field "a" of type "m.A": nullable type is nullable`},
		"UndeclaredObjectType": {field: &schema.ObjectType{Name: "m.B"}, error: `field "a" of type "m.A": type "m.B" is not declared`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := schema.New([]*schema.ObjectType{{Name: "m.A", Fields: []schema.Field{{Name: "a", Type: test.field}}}}, nil)
			assert.EqualError(t, err, test.error)
		})
	}
}

func TestTypeEqual(t *testing.T) {
	object := &schema.ObjectType{Name: "a.B"}
	for name, test := range map[string]struct {
		left, right schema.Type
		equal       bool
	}{
		"Scalar":              {left: schema.StringType{}, right: schema.StringType{}, equal: true},
		"DifferentScalar":     {left: schema.StringType{}, right: schema.NumberType{}},
		"EnumIgnoresAlias":    {left: schema.EnumType{Name: Some(schema.TypeRef("A")), Literals: []string{"a", "b"}}, right: schema.EnumType{Literals: []string{"b", "a"}}, equal: true},
		"EnumLiterals":        {left: schema.EnumType{Literals: []string{"a"}}, right: schema.EnumType{Literals: []string{"b"}}},
		"NumberLiterals":      {left: schema.NumberType{Literals: []string{"NaN"}}, right: schema.NumberType{}},
		"ObjectByName":        {left: object, right: &schema.ObjectType{Name: "a.B"}, equal: true},
		"ObjectDifferentName": {left: object, right: &schema.ObjectType{Name: "a.C"}},
		"Nullable":            {left: schema.NullableType{Type: object}, right: schema.NullableType{Type: object}, equal: true},
		"NullableVersusPlain": {left: schema.NullableType{Type: object}, right: object},
		"ListElement":         {left: schema.ListType{Element: schema.StringType{}}, right: schema.ListType{Element: schema.BooleanType{}}},
		"ListVersusMap":       {left: schema.ListType{Element: schema.StringType{}}, right: schema.MapType{Element: schema.StringType{}}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.equal, test.left.Equal(test.right))
		})
	}
}

// schemaTypes returns the types the test's sources declare, without the built-in ones.
func schemaTypes(loaded *schema.Schema) []*schema.ObjectType {
	return slices.DeleteFunc(loaded.Types(), func(declared *schema.ObjectType) bool {
		return strings.HasPrefix(string(declared.Name), "spectre.")
	})
}

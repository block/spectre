package javascript_test

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison/javascript"
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
			spectre.ingress.http("GET /v1/forecast").match<v1.Response>();
			spectre.ingress.http("GET /v2/forecast").match<v1.Response>();
			spectre.egress.http("GET weather.example/v1/forecast").match<v1.Response>();
			spectre.field<v1.Response, "value">(() => "fixed");
		`),
		"users/user.ts": script(`spectre.message<v1.User>((user) => user);`),
	})
	assert.NoError(t, err)

	typed := javascript.ActionMatch{Type: Some[schema.TypeRef]("test.v1.Response")}
	assert.Equal(t, []rule{{`http("GET /v1/forecast")`, typed}, {`http("GET /v2/forecast")`, typed}}, rules(program))
	assert.Equal(t, []endpoint{{"http", "GET weather.example/v1/forecast", "test.v1.Response"}}, endpoints(program, model.Egress))
	assert.Equal(t, []javascript.FieldTarget{javascript.NewFieldTarget("test.v1.Response", "value")}, program.Fields())
	assert.Equal(t, []schema.TypeRef{"test.v1.User"}, program.Messages())
	operation, err := program.Schema().Operation("test.v1.UserService.GetUser")
	assert.NoError(t, err)
	assert.Equal(t, "test.v1.User", operation.Request)
}

func TestRegistersRulesInOrder(t *testing.T) {
	program, err := compile(t, fstest.MapFS{
		"rules.ts": script(`
			spectre.ingress.http("GET /_status").ignore();
			spectre.ingress.http("/").status(500, 503).match<spectre.Text>(({ status }) => ({ status }));
			spectre.ingress.http((request) => request.path.startsWith("/v2/")).status((status) => status >= 400).ignore();
			const forecast = spectre.ingress.http("GET /v1/forecast");
			forecast.status(404).match<v1.User>();
			forecast.match<v1.Response>();
			spectre.ingress.http("/").responseHeader("x-served-by", "rails").requestHeader("x-synthetic", "true").status(502).responseHeader("Retry-After", (value) => value !== undefined).ignore();
		`),
	})
	assert.NoError(t, err)

	assert.Equal(t, []rule{
		{`http("GET /_status")`, javascript.ActionIgnore{}},
		{`http("/").status(500, 503)`, javascript.ActionMatch{Type: None[schema.TypeRef](), Normalises: true}},
		{`http(<function>).status(<function>)`, javascript.ActionIgnore{}},
		{`http("GET /v1/forecast").status(404)`, javascript.ActionMatch{Type: Some[schema.TypeRef]("test.v1.User")}},
		{`http("GET /v1/forecast")`, javascript.ActionMatch{Type: Some[schema.TypeRef]("test.v1.Response")}},
		{`http("/").requestHeader("X-Synthetic", "true").status(502).responseHeader("Retry-After", <function>).responseHeader("X-Served-By", "rails")`, javascript.ActionIgnore{}},
	}, rules(program))
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	evaluator.Close()
}

func TestEvaluatesRuleFunctions(t *testing.T) {
	program, err := newProgram(t, `
		spectre.ingress.http((request) => request.method === "GET" && request.path.startsWith("/v2/"))
			.status((status) => status >= 500)
			.match<v1.Response>(({ status, body }) => ({ status: status === 503 ? 500 : status, body }));
		spectre.ingress.http("/").status(() => "yes" as unknown as boolean).ignore();
		spectre.ingress.http(() => { throw new Error("no"); }).ignore();
		spectre.ingress.http("/").responseHeader("Content-Type", (type) => type === undefined || type.startsWith("text/")).ignore();
		spectre.ingress.http((request) => request.headers["x-synthetic"] === "a, b").requestHeader("Accept", (value) => value === "text/html").ignore();
	`)
	assert.NoError(t, err)
	declared := program.Rules()
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()

	for name, test := range map[string]struct {
		request  javascript.Request
		expected bool
	}{
		"Selected":    {request: javascript.Request{Method: "GET", Path: "/v2/forecast"}, expected: true},
		"OtherMethod": {request: javascript.Request{Method: "POST", Path: "/v2/forecast"}},
		"OtherPath":   {request: javascript.Request{Method: "GET", Path: "/v1/forecast"}},
	} {
		t.Run(name, func(t *testing.T) {
			selected, err := evaluator.SelectsRequest(declared[0], test.request)
			assert.NoError(t, err)
			assert.Equal(t, test.expected, selected)
		})
	}
	selected, err := evaluator.SelectsStatus(declared[0], 503)
	assert.NoError(t, err)
	assert.True(t, selected)
	selected, err = evaluator.SelectsStatus(declared[0], 404)
	assert.NoError(t, err)
	assert.False(t, selected)

	normalised, err := evaluator.NormaliseResponse(declared[0], map[string]any{"status": 503, "body": map[string]any{"value": "a"}})
	assert.NoError(t, err)
	assert.Equal(t, Some[any](map[string]any{"status": float64(500), "body": map[string]any{"value": "a"}}), normalised)

	_, err = evaluator.SelectsStatus(declared[1], 500)
	assert.EqualError(t, err, `JavaScript selector of rule http("/").status(<function>) returned a value that is not a boolean`)
	_, err = evaluator.SelectsRequest(declared[2], javascript.Request{Method: "GET", Path: "/"})
	assert.EqualError(t, err, `JavaScript selector of rule http(<function>) threw an exception`)
	_, err = evaluator.SelectsStatus(declared[2], 500)
	assert.EqualError(t, err, `JavaScript status function of rule http(<function>) is unavailable`)

	for value, expected := range map[Option[string]]bool{Some("text/html"): true, Some("application/json"): false, None[string](): true} {
		selected, err = evaluator.SelectsResponseHeader(declared[3], "Content-Type", value)
		assert.NoError(t, err)
		assert.Equal(t, expected, selected)
	}
	selected, err = evaluator.SelectsRequest(declared[4], javascript.Request{Method: "GET", Path: "/", Header: http.Header{"X-Synthetic": {"a", "b"}}})
	assert.NoError(t, err)
	assert.True(t, selected)
	selected, err = evaluator.SelectsRequest(declared[4], javascript.Request{Method: "GET", Path: "/"})
	assert.NoError(t, err)
	assert.False(t, selected)
	selected, err = evaluator.SelectsRequestHeader(declared[4], "Accept", Some("text/html"))
	assert.NoError(t, err)
	assert.True(t, selected)
	_, err = evaluator.SelectsResponseHeader(declared[4], "Accept", Some("text/html"))
	assert.EqualError(t, err, `JavaScript function for response header "Accept" of rule http(<function>).requestHeader("Accept", <function>) is unavailable`)
	_, err = evaluator.SelectsResponseHeader(declared[3], "Accept", None[string]())
	assert.EqualError(t, err, `JavaScript function for response header "Accept" of rule http("/").responseHeader("Content-Type", <function>) is unavailable`)
}

func TestLoadsEmptyScriptsDirectory(t *testing.T) {
	program, err := compile(t, fstest.MapFS{"README.md": {Data: []byte("Not a script.")}})
	assert.NoError(t, err)

	assert.Equal(t, []rule{}, rules(program))
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
	assert.Equal(t, []schema.TypeRef{"test.v1.User"}, program.Messages())
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
			files:   fstest.MapFS{"test.ts": script(`spectre.ingress.http("GET /v1/forecast").match();`)},
			message: "scripts/test.ts:1:72: spectre.IngressHTTPRule.match needs 1 explicit type argument(s)",
		},
		"IgnoreTypeArgument": {
			files:   fstest.MapFS{"test.ts": script(`spectre.ingress.http("GET /_status").ignore<v1.Response>();`)},
			message: "Expected 0 type arguments, but got 1.",
		},
		"TextMessage": {
			files:   fstest.MapFS{"test.ts": script(`spectre.message<spectre.Text>((text) => text);`)},
			message: "only ingress rules may compare bodies as Text",
		},
		"TextEgress": {
			files:   fstest.MapFS{"test.ts": script(`spectre.egress.http("GET weather.example/v1/forecast").match<spectre.Text>();`)},
			message: "only ingress rules may compare bodies as Text",
		},
		"NormaliserReturnsWrongBody": {
			files:   fstest.MapFS{"test.ts": script(`spectre.ingress.http("/").match<v1.Response>(() => ({ body: { value: 1 } }));`)},
			message: "is not assignable to parameter of type '(response: Response<Response>) => Response<Response>'",
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
			message: "scripts/test.ts:2:32: Extra augments a schema module; only schema files may declare its members",
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

func TestRejectsInvalidEndpointDeclarations(t *testing.T) {
	for name, test := range map[string]struct {
		body    string
		message string
	}{
		"NoPattern":        {body: `egress.http();`, message: "spectre.egress.http requires a pattern"},
		"EmptyPattern":     {body: `egress.http("");`, message: "spectre.egress.http pattern must be a non-empty string"},
		"NonStringPattern": {body: `egress.http(1);`, message: "spectre.egress.http pattern must be a non-empty string"},
		"Function":         {body: `egress.http(() => true);`, message: "spectre.egress.http pattern must be a non-empty string"},
		"MatchNoType":      {body: `egress.http("GET weather.example/v1/forecast").match();`, message: "spectre.EgressHTTPRule.match requires a type name"},
		"MatchTwoArgs":     {body: `egress.http("GET weather.example/v1/forecast").match("test.v1.Response", () => ({}));`, message: "spectre.EgressHTTPRule.match requires a type name"},
		"MatchEmptyType":   {body: `egress.http("GET weather.example/v1/forecast").match("");`, message: "type name must be a non-empty string"},
		"Duplicate": {
			body:    `spectre.egress.http("GET weather.example/v1/forecast").match<v1.Response>(); spectre.egress.http("GET weather.example/v1/forecast").match<v1.User>();`,
			message: `duplicate endpoint "GET weather.example/v1/forecast"`,
		},
		"EgressWithoutHost": {
			body:    `spectre.egress.http("GET /v1/forecast").match<v1.Response>();`,
			message: `spectre.egress.http pattern "GET /v1/forecast" must have the form "<METHOD> <host>/<path>"`,
		},
		"EgressWithoutMethod": {
			body:    `spectre.egress.http("weather.example/v1/forecast").match<v1.Response>();`,
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

func TestRejectsInvalidRules(t *testing.T) {
	for name, test := range map[string]struct {
		body    string
		message string
	}{
		"NoSelector":       {body: `ingress.http();`, message: "spectre.ingress.http requires a pattern or a function"},
		"EmptyPattern":     {body: `ingress.http("");`, message: "spectre.ingress.http pattern must be a non-empty string"},
		"NonStringPattern": {body: `ingress.http(1);`, message: "spectre.ingress.http pattern must be a non-empty string"},
		"Host": {
			body:    `ingress.http("GET weather.example/v1/forecast");`,
			message: `spectre.ingress.http pattern "GET weather.example/v1/forecast" must have the form "[<METHOD> ]/<path>"`,
		},
		"NoPath":           {body: `ingress.http("GET");`, message: `must have the form "[<METHOD> ]/<path>"`},
		"MatchNoType":      {body: `ingress.http("/").match();`, message: "spectre.IngressHTTPRule.match requires a type name and an optional normaliser"},
		"MatchEmptyType":   {body: `ingress.http("/").match("");`, message: "spectre.IngressHTTPRule.match type name must be a non-empty string"},
		"MatchNotFunction": {body: `ingress.http("/").match("test.v1.Response", 1);`, message: "spectre.IngressHTTPRule.match normaliser must be a function"},
		"MatchThreeArgs":   {body: `ingress.http("/").match("test.v1.Response", () => ({}), 1);`, message: "requires a type name and an optional normaliser"},
		"IgnoreArgument":   {body: `ingress.http("/").ignore(1);`, message: "spectre.IngressHTTPRule.ignore takes no arguments"},
		"StatusNone":       {body: `ingress.http("/").status();`, message: "spectre.IngressHTTPRule.status requires status codes or a function"},
		"StatusBelowRange": {body: `ingress.http("/").status(99);`, message: "spectre.IngressHTTPRule.status code 99 must be an integer from 100 to 599"},
		"StatusAboveRange": {body: `ingress.http("/").status(600);`, message: "must be an integer from 100 to 599"},
		"StatusFraction":   {body: `ingress.http("/").status(200.5);`, message: "must be an integer from 100 to 599"},
		"StatusString":     {body: `ingress.http("/").status("200");`, message: "must be an integer from 100 to 599"},
		"StatusRepeated":   {body: `ingress.http("/").status(500, 500);`, message: "spectre.IngressHTTPRule.status code 500 is repeated"},
		"StatusTwice":      {body: `ingress.http("/").status(500).status(503);`, message: "spectre.IngressHTTPRule.status is already set for this rule"},
		"StatusMixed":      {body: `ingress.http("/").status(() => true, 500);`, message: "takes either status codes or one function"},
		"Duplicate":        {body: `ingress.http("GET /_status").ignore(); ingress.http("GET /_status").match("test.v1.Response");`, message: `duplicate rule http("GET /_status")`},
		"DuplicateStatus":  {body: `ingress.http("/").status(500).ignore(); ingress.http("/").status(500).ignore();`, message: `duplicate rule http("/").status(500)`},
		"HeaderNoValue":    {body: `ingress.http("/").responseHeader("Accept");`, message: "spectre.IngressHTTPRule.responseHeader requires a header name and a value or a function"},
		"HeaderEmptyName":  {body: `ingress.http("/").responseHeader("", "x");`, message: "spectre.IngressHTTPRule.responseHeader header name must be a non-empty string"},
		"HeaderBadName":    {body: `ingress.http("/").responseHeader("Content Type", "x");`, message: `spectre.IngressHTTPRule.responseHeader header name "Content Type" is not valid`},
		"HeaderNotString":  {body: `ingress.http("/").responseHeader("Accept", 1);`, message: "spectre.IngressHTTPRule.responseHeader value must be a string or a function"},
		"RequestHeaderTwice": {
			body:    `ingress.http("/").requestHeader("Accept", "a").requestHeader("ACCEPT", "b");`,
			message: `spectre.IngressHTTPRule.requestHeader header "Accept" is already set for this rule`,
		},
		"RequestHeaderBadName": {body: `ingress.http("/").requestHeader("a:b", "x");`, message: `spectre.IngressHTTPRule.requestHeader header name "a:b" is not valid`},
		"DuplicateRequestHeaders": {
			body:    `ingress.http("/").requestHeader("A", "1").ignore(); ingress.http("/").requestHeader("a", "1").ignore();`,
			message: `duplicate rule http("/").requestHeader("A", "1")`,
		},
		"HeaderTwice": {body: `ingress.http("/").responseHeader("Accept", "a").responseHeader("accept", () => true);`, message: `spectre.IngressHTTPRule.responseHeader header "Accept" is already set for this rule`},
		"DuplicateHeaders": {
			body:    `ingress.http("/").responseHeader("A", "1").responseHeader("B", "2").ignore(); ingress.http("/").responseHeader("b", "2").responseHeader("a", "1").ignore();`,
			message: `duplicate rule http("/").responseHeader("A", "1").responseHeader("B", "2")`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newProgram(t, untyped+test.body)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestAcceptsRulesThatDifferOnlyByFunction(t *testing.T) {
	_, err := newProgram(t, untyped+`
		ingress.http("/").status(500).ignore();
		ingress.http("/").status(500, 503).ignore();
		ingress.http("/").status(() => true).ignore();
		ingress.http("/").status(() => true).ignore();
		ingress.http(() => true).ignore();
		ingress.http(() => true).ignore();
		ingress.http("/").responseHeader("Accept", "a").ignore();
		ingress.http("/").responseHeader("Accept", "b").ignore();
		ingress.http("/").responseHeader("Accept", "a").responseHeader("Vary", "c").ignore();
		ingress.http("/").responseHeader("Accept", () => true).ignore();
		ingress.http("/").responseHeader("Accept", () => true).ignore();
		ingress.http("/").requestHeader("Accept", "a").ignore();
		ingress.http("/").requestHeader("Accept", "a").responseHeader("Accept", "a").ignore();
	`)
	assert.NoError(t, err)
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
	assert.Equal(t, []schema.TypeRef{"test.v1.User"}, program.Messages())
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	normalised, err := evaluator.NormaliseMessage("test.v1.User", Some[any]("alice"))
	assert.NoError(t, err)
	assert.Equal(t, Some[any]("alice"), normalised)
}

func TestTimeoutBoundsOnlyEvaluation(t *testing.T) {
	_, err := javascript.NewProgram(t.Context(), 10*time.Millisecond, declarations(), fstest.MapFS{"test.ts": script(`while (true) {}`)})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "evaluate comparison modules")
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
		value    Option[any]
		expected any
	}{
		"Missing": {value: None[any](), expected: "missing"},
		"Null":    {value: Some[any](nil), expected: "null"},
		"Value":   {value: Some[any]("value"), expected: "other"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			normalised, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "owner"), test.value)

			assert.NoError(t, err)
			assert.Equal(t, Some(test.expected), normalised)
		})
	}
}

func TestNormaliserResults(t *testing.T) {
	type result struct {
		Normalised Option[any]
		Failed     bool
	}
	tests := map[string]struct {
		normaliser string
		expected   result
	}{
		"Sorted":                   {normaliser: `(value) => value.sort()`, expected: result{Normalised: Some[any]([]any{"a", "b"})}},
		"Constant":                 {normaliser: `() => true`, expected: result{Normalised: Some[any](true)}},
		"Null":                     {normaliser: `() => null`, expected: result{Normalised: Some[any](nil)}},
		"UndefinedRemoves":         {normaliser: `() => undefined`},
		"UndefinedPropertyOmitted": {normaliser: `() => ({kept: 1, dropped: undefined})`, expected: result{Normalised: Some[any](map[string]any{"kept": float64(1)})}},
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

			normalised, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "values"), Some[any]([]any{"b", "a"}))

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

	normalised, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), Some[any]("original"))

	assert.NoError(t, err)
	assert.Equal(t, Some[any]("original"), normalised)
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

	normalised, err := evaluator.NormaliseMessage("test.v1.Response", Some[any](value))

	assert.NoError(t, err)
	assert.Equal(t, Some[any](map[string]any{"value": "changed"}), normalised)
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
		normalised Option[any]
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
			normalised, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), Some[any]("same"))
			results <- result{normalised: normalised, err: err}
		})
	}
	wait.Wait()
	close(results)

	for result := range results {
		assert.NoError(t, result.err)
		assert.Equal(t, Some[any]("same:1"), result.normalised)
	}
}

func TestTranspilesTypeScriptModules(t *testing.T) {
	program, err := compile(t, fstest.MapFS{
		"main.ts": script(`
			import { prefix, suffix } from "./lib/helpers";
			import type { Greeting } from "./lib/types";
			const greet = (value: Greeting): Greeting => prefix + value + suffix;
			spectre.ingress.http("GET /v1/forecast").match<v1.Response>();
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
	normalised, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), Some[any]("world"))
	assert.NoError(t, err)
	assert.Equal(t, Some[any]("hello world!"), normalised)
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
	program, err := javascript.NewProgram(t.Context(), time.Second, declarations(), os.DirFS(directory))
	assert.NoError(t, err)
	assert.NoError(t, os.Remove(mainPath))
	assert.NoError(t, os.WriteFile(helperPath, []byte(`throw new Error("new source");`), 0o600))
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	value, err := evaluator.NormaliseField(javascript.NewFieldTarget("test.v1.Response", "value"), Some[any]("original"))
	assert.NoError(t, err)
	assert.Equal(t, Some[any]("original!"), value)
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
	assert.Equal(t, []schema.TypeRef{"test.v1.User"}, program.Messages())
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
			_, err := javascript.NewProgram(t.Context(), time.Second, declarations(), os.DirFS(scripts))
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
			value, err := evaluator.NormaliseField(test.target, Some[any]("original"))
			assert.NoError(t, err)
			assert.Equal(t, Some[any](test.expected), value)
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

type rule struct {
	declared string
	action   javascript.Action
}

func rules(program *javascript.Program) []rule {
	declared := []rule{}
	for _, value := range program.Rules() {
		declared = append(declared, rule{value.String(), value.Action()})
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
	return javascript.NewProgram(t.Context(), time.Second, declarations(), files)
}

func declarations() fstest.MapFS {
	return fstest.MapFS{"test.d.ts": {Data: []byte(testDeclarations)}}
}

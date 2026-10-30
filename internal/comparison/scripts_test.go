package comparison_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/schema"
)

// declarations mirror the ProtoJSON shape of the test.v1 protobuf package, plus a
// raw module whose optional fields have no implicit defaults.
const declarations = `
declare module "test.v1" {
  export interface Request {}
  export interface Filter { minDays?: number; }
  export interface Query {
    location?: string;
    days?: number;
    tags: string[];
    filter?: import("test.v1").Filter;
    trace?: string;
  }
  export interface User { name: string; }
  export interface Response {
    stable: string;
    ignored?: string;
    roles: string[];
    users: import("test.v1").User[];
    count: string;
    owner?: import("test.v1").User;
    labels: Record<string, string>;
  }
  export interface Service {
    Get(request: import("test.v1").Request): import("test.v1").Response;
  }
}

declare module "raw" {
  interface Response {
    stable: string;
    ignored?: string;
    count?: number;
    enabled?: boolean;
    roles?: string[];
    metadata?: Record<string, string>;
  }
}
`

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := map[string]func(*comparison.Config){
		"ScriptsDir": func(config *comparison.Config) { config.ScriptsDir = "" },
		"SchemaDir":  func(config *comparison.Config) { config.Schema.SchemaDir = "" },
		"Timeout":    func(config *comparison.Config) { config.ComparisonTimeout = 0 },
	}
	for name, update := range tests {
		t.Run(name, func(t *testing.T) {
			config := newConfig(t, map[string]string{"test.ts": module("")})
			update(&config)
			scripts, err := comparison.New(t.Context(), config)
			assert.Error(t, err)
			assert.Equal(t, (*comparison.Scripts)(nil), scripts)
		})
	}
}

func TestRejectsInvalidScripts(t *testing.T) {
	for name, test := range map[string]struct {
		scripts map[string]string
		message string
	}{
		"MissingSpectreImport": {
			scripts: map[string]string{"test.ts": `
				import type * as v1 from "test.v1";
				spectre.field<v1.Response, "ignored">(() => undefined);
			`},
			message: "Cannot find name 'spectre'",
		},
		"UnsupportedModuleImport": {
			scripts: map[string]string{"test.ts": `import "unsupported";`},
			message: "compile comparison scripts",
		},
		"IngressHost": {
			scripts: map[string]string{"weather.ts": module(`spectre.ingress.http("GET weather.example/v1/forecast").match<v1.Response>();`)},
			message: `must have the form "[<METHOD> ]/<path>"`,
		},
		"DuplicateRuleAcrossScripts": {
			scripts: map[string]string{
				"a.ts": module(`spectre.ingress.http("GET /v1/forecast").match<v1.Response>();`),
				"b.ts": module(`spectre.ingress.http("GET /v1/forecast").match<v1.Response>();`),
			},
			message: `duplicate rule http("GET /v1/forecast")`,
		},
		"DuplicateTargetAcrossScripts": {
			scripts: map[string]string{
				"a.ts": module(`spectre.message<v1.User>((user) => user);`),
				"b.ts": module(`spectre.message<v1.User>((user) => user);`),
			},
			message: `duplicate normaliser target "test.v1.User"`,
		},
		// The cast skips type checking, so the plan's own check rejects the path.
		"UnknownNormaliserField": {
			scripts: map[string]string{"test.ts": module(`(spectre.field as any)("test.v1.Response", "unknown", () => true);`)},
			message: `prepare comparison plan`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			scripts, err := comparison.New(t.Context(), newConfig(t, test.scripts))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
			assert.Equal(t, (*comparison.Scripts)(nil), scripts)
		})
	}
}

func TestRejectsEndpointsAbsentFromSchema(t *testing.T) {
	for name, test := range map[string]struct {
		typeName string
		pattern  string
	}{
		"Missing":   {typeName: "test.v1.Missing", pattern: "GET weather.example/v1/forecast"},
		"Operation": {typeName: "test.v1.Service.Get", pattern: "GET weather.example/v1/forecast"},
		"NotAName":  {typeName: "not a name", pattern: "GET weather.example/v1/forecast"},
	} {
		t.Run(name, func(t *testing.T) {
			// The cast skips type checking, so the type reaches the schema check.
			config := newConfig(t, map[string]string{
				"weather.ts": module(`(spectre.egress.http("` + test.pattern + `").match as any)("` + test.typeName + `");`),
			})
			_, err := comparison.New(t.Context(), config)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `resolve endpoint "`+test.pattern+`"`)
			assert.Contains(t, err.Error(), `type "`+test.typeName+`" is not declared`)
		})
	}
}

func TestRejectsRulesAbsentFromSchema(t *testing.T) {
	// The cast skips type checking, so the type reaches the schema check.
	config := newConfig(t, map[string]string{
		"weather.ts": module(`(spectre.ingress.http("GET /v1/forecast").match as any)("test.v1.Missing");`),
	})
	_, err := comparison.New(t.Context(), config)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `resolve rule http("GET /v1/forecast")`)
	assert.Contains(t, err.Error(), `type "test.v1.Missing" is not declared`)
}

func TestSelectsTheFirstRuleThatSelectsAResponse(t *testing.T) {
	scripts := newScripts(t, `
		spectre.ingress.http("/").status(500).match<spectre.Text>();
		spectre.ingress.http((request) => request.path.startsWith("/v2/")).status((status) => status >= 400).ignore();
		spectre.ingress.http("/").status(503).responseHeader("Retry-After", (value) => value !== undefined).responseHeader("x-served-by", "rails").ignore();
		spectre.ingress.http("/").responseHeader("Content-Type", (type) => type?.startsWith("text/html") ?? false).match<spectre.Text>();
		spectre.ingress.http("/v3/").requestHeader("X-Synthetic", "true").requestHeader("Accept", (value) => value === undefined).ignore();
		spectre.ingress.http("GET /v1/forecast").match<v1.Response>();
	`)
	rules := scripts.Rules()
	v3 := func(header http.Header) javascript.Request {
		return javascript.Request{Method: "GET", Path: "/v3/forecast", Header: header}
	}
	v2, v1 := javascript.Request{Method: "GET", Path: "/v2/forecast"}, javascript.Request{Method: "GET", Path: "/v1/forecast"}
	html := http.Header{"Content-Type": {"text/html; charset=utf-8"}}
	retry := http.Header{"Retry-After": {"5"}, "X-Served-By": {"rails"}}
	for name, test := range map[string]struct {
		rules    []javascript.Rule
		request  javascript.Request
		response javascript.Response
		expected Option[javascript.Rule]
	}{
		"Status":          {rules: rules, request: v2, response: javascript.Response{Status: 500}, expected: Some(rules[0])},
		"Functions":       {rules: rules, request: v2, response: javascript.Response{Status: 404}, expected: Some(rules[1])},
		"Headers":         {rules: rules, request: v1, response: javascript.Response{Status: 503, Header: retry}, expected: Some(rules[2])},
		"HeaderAbsent":    {rules: rules, request: v1, response: javascript.Response{Status: 503, Header: http.Header{"X-Served-By": {"rails"}}}, expected: Some(rules[5])},
		"HeaderDiffers":   {rules: rules, request: v1, response: javascript.Response{Status: 503, Header: http.Header{"Retry-After": {"5"}, "X-Served-By": {"go"}}}, expected: Some(rules[5])},
		"HeaderJoined":    {rules: rules, request: v1, response: javascript.Response{Status: 503, Header: http.Header{"Retry-After": {"5"}, "X-Served-By": {"rails", "go"}}}, expected: Some(rules[5])},
		"HeaderFunction":  {rules: rules, request: v1, response: javascript.Response{Status: 200, Header: html}, expected: Some(rules[3])},
		"RequestHeaders":  {rules: rules[4:], request: v3(http.Header{"X-Synthetic": {"true"}}), response: javascript.Response{Status: 200}, expected: Some(rules[4])},
		"RequestValue":    {rules: rules[4:5], request: v3(http.Header{"X-Synthetic": {"false"}}), response: javascript.Response{Status: 200}, expected: None[javascript.Rule]()},
		"RequestFunction": {rules: rules[4:5], request: v3(http.Header{"X-Synthetic": {"true"}, "Accept": {"*/*"}}), response: javascript.Response{Status: 200}, expected: None[javascript.Rule]()},
		"RequestRejected": {rules: rules, request: v1, response: javascript.Response{Status: 404}, expected: Some(rules[5])},
		"StatusRejected":  {rules: rules[:2], request: v2, response: javascript.Response{Status: 200}, expected: None[javascript.Rule]()},
		"NoRules":         {request: javascript.Request{Method: "GET", Path: "/"}, response: javascript.Response{Status: 200}, expected: None[javascript.Rule]()},
	} {
		t.Run(name, func(t *testing.T) {
			selected, err := scripts.SelectRule(t.Context(), test.rules, test.request, test.response)
			assert.NoError(t, err)
			assert.Equal(t, test.expected, selected)
		})
	}
}

func TestNormalisesResponsesWithRules(t *testing.T) {
	scripts := newScripts(t, `
		spectre.ingress.http("/typed").match<v1.Response>(({ status, body }) => ({ status: status === 201 ? 200 : status, body: body && { ...body, ignored: undefined } }));
		spectre.ingress.http("/text").match<spectre.Text>(({ body }) => ({ body: body?.trim() }));
		spectre.ingress.http("/plain").match<v1.Response>();
		const unchecked = <T,>(normalise: unknown) => normalise as (response: spectre.Response<T>) => spectre.Response<T>;
		spectre.ingress.http("/array").match<v1.Response>(unchecked(() => []));
		spectre.ingress.http("/member").match<v1.Response>(unchecked(() => ({ headers: {} })));
		spectre.ingress.http("/status").match<v1.Response>(unchecked(() => ({ status: 200.5 })));
		spectre.ingress.http("/body").match<v1.Response>(unchecked(() => ({ body: { stable: 1 } })));
		spectre.ingress.http("/textbody").match<spectre.Text>(unchecked<spectre.Text>(() => ({ body: 1 })));
	`)
	rules := scripts.Rules()
	body := decode(t, `{"stable":"same","ignored":"first","roles":[],"users":[],"count":"0","labels":{}}`)
	for name, test := range map[string]struct {
		rule    javascript.Rule
		body    Option[any]
		status  Option[int]
		result  Option[any]
		message string
	}{
		"Typed":      {rule: rules[0], body: Some(body), status: Some(200), result: Some(decode(t, `{"stable":"same","roles":[],"users":[],"count":"0","labels":{}}`))},
		"Text":       {rule: rules[1], body: Some[any](" page "), status: None[int](), result: Some[any]("page")},
		"NoBody":     {rule: rules[1], body: None[any](), status: None[int](), result: None[any]()},
		"Unchanged":  {rule: rules[2], body: Some(body), status: Some(201), result: Some(body)},
		"NotObject":  {rule: rules[3], body: Some(body), message: "response normaliser must return an object"},
		"Member":     {rule: rules[4], body: Some(body), message: `normalised response has an unknown member "headers"`},
		"Status":     {rule: rules[5], body: Some(body), message: "normalised status must be an integer from 100 to 599"},
		"WrongBody":  {rule: rules[6], body: Some(body), message: "validate normalised body"},
		"TextNumber": {rule: rules[7], body: Some[any]("page"), message: "normalised Text body must be a string"},
	} {
		t.Run(name, func(t *testing.T) {
			status, result, err := scripts.NormaliseResponse(t.Context(), test.rule, 201, test.body)
			if test.message != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), test.message)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.status, status)
			assert.Equal(t, test.result, result)
		})
	}
}

func TestLoadsEveryScriptAsOneSet(t *testing.T) {
	scripts := newScriptsFromFiles(t, map[string]string{
		"users/user.ts": module(`
			import {lower} from "../helpers/strings";
			spectre.message<v1.User>((user) => user === undefined ? undefined : {name: lower(user.name)});
			spectre.egress.http("GET weather.example/v1/forecast").match<v1.Query>();
		`),
		"weather.ts": module(`
			import {lower} from "./helpers/strings";
			spectre.ingress.http("GET /v1/forecast").match<v1.Response>();
			spectre.ingress.http("GET /_status").ignore();
			spectre.field<v1.Response, "stable">((stable) => stable === undefined ? undefined : lower(stable));
		`),
		"helpers/strings.ts": `export const lower = (value: string) => value.toLowerCase();`,
		"README.md":          "Not a script.",
	})
	endpoints := []string{}
	for _, endpoint := range scripts.Endpoints() {
		endpoints = append(endpoints, fmt.Sprintf("%s %s %q %q", endpoint.Direction(), endpoint.Protocol(), endpoint.Pattern(), endpoint.Type()))
	}
	assert.Equal(t, []string{`egress http "GET weather.example/v1/forecast" "test.v1.Query"`}, endpoints)
	type rule struct {
		declared string
		action   javascript.Action
	}
	rules := []rule{}
	for _, declared := range scripts.Rules() {
		rules = append(rules, rule{declared.String(), declared.Action()})
	}
	assert.Equal(t, []rule{
		{`http("GET /v1/forecast")`, javascript.ActionMatch{Type: Some[schema.TypeRef]("test.v1.Response")}},
		{`http("GET /_status")`, javascript.ActionIgnore{}},
	}, rules)

	differences, err := scripts.Compare(t.Context(), Some[schema.TypeRef]("test.v1.Response"),
		decode(t, `{"stable":"SAME","owner":{"name":"ALICE"}}`),
		decode(t, `{"stable":"same","owner":{"name":"alice"}}`))
	assert.NoError(t, err)
	assert.Equal(t, []string{}, differences)
}

func TestCompareAppliesNormalisers(t *testing.T) {
	response := Some[schema.TypeRef]("test.v1.Response")
	for name, test := range map[string]struct {
		script    string
		typeName  Option[schema.TypeRef]
		reference string
		candidate string
		expected  []string
	}{
		"IgnoreAndSortFields": {
			typeName: response,
			script: `
				spectre.field<v1.Response, "ignored">(() => undefined);
				spectre.field<v1.Response, "roles">((roles) => roles.sort());
			`,
			reference: `{"stable":"same","ignored":"first","roles":["reader","writer"]}`,
			candidate: `{"roles":["writer","reader"],"ignored":"second","stable":"same"}`,
		},
		"Int64IsJavaScriptString": {
			typeName:  response,
			script:    `spectre.field<v1.Response, "count">((count) => typeof count);`,
			reference: `{"count":"9007199254740993"}`,
			candidate: `{"count":"9007199254740994"}`,
		},
		"UndefinedForMissingField": {
			typeName:  response,
			script:    `spectre.field<v1.Response, "ignored">((value) => value === undefined ? "present" : value);`,
			reference: `{"stable":"same"}`,
			candidate: `{"stable":"same","ignored":"present"}`,
		},
		"DifferencesRemainAfterNormalisation": {
			typeName:  response,
			script:    `spectre.field<v1.Response, "ignored">((value) => value?.toLowerCase());`,
			reference: `{"stable":"same","ignored":"First"}`,
			candidate: `{"stable":"same","ignored":"second"}`,
			expected:  []string{"$.ignored"},
		},
		"ParentReceivesNormalisedChildren": {
			typeName: response,
			script: `
				spectre.field<v1.Response, "ignored">((value) => value?.toLowerCase());
				// Normalisers may return any JSON, so the result need not be a Response.
				spectre.message<v1.Response>((response): any => ({ignored: response?.ignored}));
			`,
			reference: `{"stable":"first","ignored":"SAME"}`,
			candidate: `{"stable":"second","ignored":"same"}`,
		},
		"MessageNormaliserCanRemoveRoot": {
			typeName:  response,
			script:    `spectre.message<v1.Response>(() => undefined);`,
			reference: `{"stable":"first"}`,
			candidate: `{"stable":"second"}`,
		},
		"FieldNormaliserReplacesMessageNormaliserAtSameLocation": {
			typeName: response,
			script: `
				spectre.message<v1.User>(() => { throw new Error("message normaliser ran"); });
				spectre.field<v1.Response, "owner">((owner): any => owner === undefined ? undefined : owner.name.toLowerCase());
			`,
			reference: `{"owner":{"name":"ALICE"}}`,
			candidate: `{"owner":{"name":"alice"}}`,
		},
		"MessageNormaliserReceivesUndefinedForMissingMessage": {
			typeName:  response,
			script:    `spectre.message<v1.User>((): any => "ignored");`,
			reference: `{"stable":"same","owner":{"name":"alice"}}`,
			candidate: `{"stable":"same"}`,
		},
		"FieldNormaliserAppliesToRepeatedMessageElements": {
			typeName:  response,
			script:    `spectre.field<v1.Response, "users[].name">((name) => name.toLowerCase());`,
			reference: `{"stable":"same","users":[{"name":"ALICE"},{"name":"BOB"}]}`,
			candidate: `{"stable":"same","users":[{"name":"alice"},{"name":"bob"}]}`,
		},
		"MessageNormaliserCanRemoveRepeatedElement": {
			typeName: response,
			script: `spectre.message<v1.User>((user) =>
				user === undefined || user.name === "ignored" ? undefined : {name: user.name.toLowerCase()});`,
			reference: `{"users":[{"name":"ALICE"},{"name":"ignored"},{"name":"BOB"}]}`,
			candidate: `{"users":[{"name":"alice"},{"name":"bob"}]}`,
		},
		"WithoutNormalisers": {
			typeName:  response,
			reference: `{"stable":"first"}`,
			candidate: `{"stable":"second"}`,
			expected:  []string{"$.stable"},
		},
		"UntypedSkipsNormalisers": {
			script:    `spectre.field<v1.Response, "ignored">(() => undefined);`,
			typeName:  None[schema.TypeRef](),
			reference: `{"stable":"same","ignored":"first"}`,
			candidate: `{"stable":"same","ignored":"second"}`,
			// Untyped keys are payload data, so no path quotes them.
			expected: []string{"$[*]"},
		},
		"MapKeysAreNotReported": {
			typeName:  response,
			reference: `{"stable":"same","labels":{"alice@example.com":"a"}}`,
			candidate: `{"stable":"same","labels":{"alice@example.com":"b","bob@example.com":"c"}}`,
			expected:  []string{"$.labels[*]"},
		},
		"DeclaredFieldsUnderListsAreReported": {
			typeName:  response,
			reference: `{"stable":"same","users":[{"name":"alice"}]}`,
			candidate: `{"stable":"same","users":[{"name":"bob"}]}`,
			expected:  []string{"$.users[0].name"},
		},
		"ExplicitDefaultsRemainPresent": {
			typeName:  Some[schema.TypeRef]("raw.Response"),
			reference: `{"stable":"same"}`,
			candidate: `{"stable":"same","ignored":"","count":0,"enabled":false,"roles":[],"metadata":{}}`,
			expected:  []string{"$.count", "$.enabled", "$.ignored", "$.metadata", "$.roles"},
		},
		"FieldNormaliserMayChangeType": {
			script:    `spectre.field<raw.Response, "stable">((): any => ({replacement: true}));`,
			typeName:  Some[schema.TypeRef]("raw.Response"),
			reference: `{"stable":"first"}`,
			candidate: `{"stable":"second"}`,
		},
		"MessageNormaliserMayChangeType": {
			script:    `spectre.message<raw.Response>((): any => [true, null, {replacement: "value"}]);`,
			typeName:  Some[schema.TypeRef]("raw.Response"),
			reference: `{"stable":"first"}`,
			candidate: `{"stable":"second"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			scripts := newScripts(t, test.script)
			expected := test.expected
			if expected == nil {
				expected = []string{}
			}

			differences, err := scripts.Compare(t.Context(), test.typeName, decode(t, test.reference), decode(t, test.candidate))

			assert.NoError(t, err)
			assert.Equal(t, expected, differences)
		})
	}
}

func TestCompareRejectsNonJSONNormaliserResult(t *testing.T) {
	scripts := newScripts(t, `spectre.message<v1.Response>((): any => () => true);`)

	differences, err := scripts.Compare(t.Context(), Some[schema.TypeRef]("test.v1.Response"), decode(t, `{"stable":"same"}`), decode(t, `{"stable":"same"}`))

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "normalise reference")
	assert.Equal(t, []string(nil), differences)
}

func TestInterruptsRunawayNormaliser(t *testing.T) {
	config := newConfig(t, map[string]string{
		"test.ts": module(`spectre.message<v1.Response>(() => { while (true) {} });`),
	})
	config.ComparisonTimeout = 10 * time.Millisecond
	scripts, err := comparison.New(t.Context(), config)
	assert.NoError(t, err)

	_, err = scripts.Compare(t.Context(), Some[schema.TypeRef]("test.v1.Response"), decode(t, `{"stable":"same"}`), decode(t, `{"stable":"same"}`))
	assert.Error(t, err)

	_, err = scripts.Hash(t.Context(), "identity", Some[schema.TypeRef]("test.v1.Response"), "reference", decode(t, `{"stable":"same"}`))
	assert.Error(t, err)
}

func TestLogsIndividualNormaliserResults(t *testing.T) {
	var output bytes.Buffer
	ctx := logger.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	config := newConfig(t, map[string]string{"test.ts": module(`
		spectre.field<v1.Response, "ignored">(() => undefined);
		spectre.field<v1.Response, "stable">((value) => value);
	`)})
	scripts, err := comparison.New(ctx, config)
	assert.NoError(t, err)

	differences, err := scripts.Compare(ctx, Some[schema.TypeRef]("test.v1.Response"),
		decode(t, `{"stable":"first","ignored":"first"}`),
		decode(t, `{"stable":"second","ignored":"second"}`))

	assert.NoError(t, err)
	assert.Equal(t, []string{"$.stable"}, differences)
	logs := output.String()
	assert.Contains(t, logs, `"msg":"Payload normaliser completed","kind":"field","target":"test.v1.Response.ignored","side":"reference","payload_path":"$.ignored"`)
	assert.Contains(t, logs, `"msg":"Payload normaliser completed","kind":"field","target":"test.v1.Response.stable","side":"candidate","payload_path":"$.stable"`)
	assert.Contains(t, logs, `"msg":"Payload normalisation completed","message":"test.v1.Response","side":"reference","normalisers":2`)
}

func TestLogsHashNormalisationWithoutNormalisers(t *testing.T) {
	var output bytes.Buffer
	ctx := logger.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	scripts, err := comparison.New(ctx, newConfig(t, map[string]string{"test.ts": module("")}))
	assert.NoError(t, err)

	_, err = scripts.Hash(ctx, "identity", Some[schema.TypeRef]("test.v1.Query"), "candidate", decode(t, `{"location":"london"}`))

	assert.NoError(t, err)
	assert.Contains(t, output.String(), `"level":"DEBUG","msg":"Payload normalisation completed","message":"test.v1.Query","side":"candidate","normalisers":0`)
}

const queryScript = `
	spectre.field<v1.Query, "trace">(() => undefined);
	spectre.field<v1.Query, "tags">((tags) => tags === undefined ? undefined : tags.sort());
`

func TestHashesIdentityAndCanonicalJSON(t *testing.T) {
	scripts := newScripts(t, queryScript)
	identity := "http POST weather.example/v1/search"

	hash, err := scripts.Hash(t.Context(), identity, Some[schema.TypeRef]("test.v1.Query"), "reference", decode(t, `{"trace":"ignored","location":"london","days":3}`))

	assert.NoError(t, err)
	expected := sha256.Sum256([]byte(identity + "\x00" + `{"days":3,"location":"london"}`))
	assert.Equal(t, comparison.Hash(expected), hash)
}

func TestHashIsStable(t *testing.T) {
	type hashed struct {
		identity string
		typeName Option[schema.TypeRef]
		payload  string
	}
	query := func(payload string) hashed {
		return hashed{identity: "http POST weather.example/v1/search", typeName: Some[schema.TypeRef]("test.v1.Query"), payload: payload}
	}
	scripts := newScripts(t, queryScript)
	for name, test := range map[string]struct {
		left  hashed
		right hashed
		equal bool
	}{
		"FieldOrder": {
			left:  query(`{"location":"london","days":3}`),
			right: query(`{"days":3,"location":"london"}`),
			equal: true,
		},
		"IgnoredField": {
			left:  query(`{"location":"london","trace":"first"}`),
			right: query(`{"location":"london","trace":"second"}`),
			equal: true,
		},
		"SortedCollection": {
			left:  query(`{"tags":["wind","rain"]}`),
			right: query(`{"tags":["rain","wind"]}`),
			equal: true,
		},
		"Value": {
			left:  query(`{"location":"london","days":3}`),
			right: query(`{"location":"london","days":4}`),
		},
		"ExplicitDefault": {
			left:  query(`{"location":"london"}`),
			right: query(`{"location":"london","days":0}`),
		},
		"Identity": {
			left:  query(`{}`),
			right: hashed{identity: "http GET weather.example/v1/search", typeName: Some[schema.TypeRef]("test.v1.Query"), payload: `{}`},
		},
		"UntypedSkipsNormalisers": {
			left:  hashed{identity: "http POST weather.example/v1/search", payload: `{"trace":"first"}`},
			right: hashed{identity: "http POST weather.example/v1/search", payload: `{"trace":"second"}`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			left, err := scripts.Hash(t.Context(), test.left.identity, test.left.typeName, "reference", decode(t, test.left.payload))
			assert.NoError(t, err)
			right, err := scripts.Hash(t.Context(), test.right.identity, test.right.typeName, "candidate", decode(t, test.right.payload))
			assert.NoError(t, err)
			assert.Equal(t, test.equal, left == right)
		})
	}
}

func newConfig(t *testing.T, scripts map[string]string) comparison.Config {
	t.Helper()
	config := comparison.NewConfig()
	config.Schema.SchemaDir = writeFiles(t, map[string]string{"test.d.ts": declarations})
	config.ScriptsDir = writeFiles(t, scripts)
	return config
}

func newScriptsFromFiles(t *testing.T, files map[string]string) *comparison.Scripts {
	t.Helper()
	scripts, err := comparison.New(t.Context(), newConfig(t, files))
	assert.NoError(t, err)
	return scripts
}

func newScripts(t *testing.T, script string) *comparison.Scripts {
	t.Helper()
	return newScriptsFromFiles(t, map[string]string{"test.ts": module(script)})
}

// module imports the spectre API and the test schema modules, so body can name their types.
func module(body string) string {
	return `import * as spectre from "spectre"; import type * as v1 from "test.v1"; import type * as raw from "raw";` + body
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

// decode returns a fresh payload, because normalisation mutates it.
func decode(t *testing.T, payload string) any {
	t.Helper()
	var value any
	assert.NoError(t, json.Unmarshal([]byte(payload), &value))
	return value
}

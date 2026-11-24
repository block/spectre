package script_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/protocols/http/script"
	"github.com/block/spectre/internal/schema"
)

func TestRejectsInvalidScripts(t *testing.T) {
	for name, test := range map[string]struct {
		scripts map[string]string
		message string
	}{
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
	} {
		t.Run(name, func(t *testing.T) {
			scripts, _, err := newScripts(t, test.scripts)
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
			_, _, err := newScripts(t, map[string]string{
				"weather.ts": module(`(spectre.egress.http("` + test.pattern + `").match as any)("` + test.typeName + `");`),
			})
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `resolve endpoint "`+test.pattern+`"`)
			assert.Contains(t, err.Error(), `type "`+test.typeName+`" is not declared`)
		})
	}
}

func TestRejectsRulesAbsentFromSchema(t *testing.T) {
	// The cast skips type checking, so the type reaches the schema check.
	_, _, err := newScripts(t, map[string]string{
		"weather.ts": module(`(spectre.ingress.http("GET /v1/forecast").match as any)("test.v1.Missing");`),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "spectre.IngressHTTPRule.match")
	assert.Contains(t, err.Error(), `type "test.v1.Missing" is not declared`)
}

func TestLoadsRulesFromEveryScript(t *testing.T) {
	scripts, handle, err := newScripts(t, map[string]string{
		"users/user.ts": module(`spectre.egress.http("GET weather.example/v1/forecast").match<v1.Query>();`),
		"weather.ts": module(`
			spectre.ingress.http("GET /v1/forecast").match<v1.Response>();
			spectre.ingress.http("GET /_status").ignore();
		`),
		"README.md": "Not a script.",
	})
	assert.NoError(t, err)

	endpoints := []string{}
	for _, endpoint := range scripts.Endpoints() {
		endpoints = append(endpoints, fmt.Sprintf("%s %s %q %q", endpoint.Direction(), endpoint.Protocol(), endpoint.Pattern(), endpoint.Type()))
	}
	assert.Equal(t, []string{`egress http "GET weather.example/v1/forecast" "test.v1.Query"`}, endpoints)
	declared := []rule{}
	for _, value := range handle.Declared(scripts.Program()).Rules {
		declared = append(declared, rule{value.String(), value.Action()})
	}
	response, err := scripts.Schema().Type("test.v1.Response")
	assert.NoError(t, err)
	assert.Equal(t, []rule{
		{`http("GET /v1/forecast")`, script.ActionMatch{Type: response}},
		{`http("GET /_status")`, script.ActionIgnore{}},
	}, declared)
}

func TestSelectsTheFirstRuleThatSelectsAResponse(t *testing.T) {
	scripts, handle := loadScripts(t, `
		spectre.ingress.http("/").status(500).match<spectre.Text>();
		spectre.ingress.http((request) => request.path.startsWith("/v2/")).status((status) => status >= 400).ignore();
		spectre.ingress.http("/").status(503).responseHeader("Retry-After", (value) => value !== undefined).responseHeader("x-served-by", "rails").ignore();
		spectre.ingress.http("/").responseHeader("Content-Type", (type) => type?.startsWith("text/html") ?? false).match<spectre.Text>();
		spectre.ingress.http("/v3/").requestHeader("X-Synthetic", "true").requestHeader("Accept", (value) => value === undefined).ignore();
		spectre.ingress.http("GET /v1/forecast").match<v1.Response>();
	`)
	rules := handle.Declared(scripts.Program()).Rules
	v3 := func(header http.Header) script.Request {
		return script.Request{Method: "GET", Path: "/v3/forecast", Header: header}
	}
	v2, v1 := script.Request{Method: "GET", Path: "/v2/forecast"}, script.Request{Method: "GET", Path: "/v1/forecast"}
	html := http.Header{"Content-Type": {"text/html; charset=utf-8"}}
	retry := http.Header{"Retry-After": {"5"}, "X-Served-By": {"rails"}}
	for name, test := range map[string]struct {
		rules    []script.Rule
		request  script.Request
		response script.Response
		expected Option[script.Rule]
	}{
		"Status":          {rules: rules, request: v2, response: script.Response{Status: 500}, expected: Some(rules[0])},
		"Functions":       {rules: rules, request: v2, response: script.Response{Status: 404}, expected: Some(rules[1])},
		"Headers":         {rules: rules, request: v1, response: script.Response{Status: 503, Header: retry}, expected: Some(rules[2])},
		"HeaderAbsent":    {rules: rules, request: v1, response: script.Response{Status: 503, Header: http.Header{"X-Served-By": {"rails"}}}, expected: Some(rules[5])},
		"HeaderDiffers":   {rules: rules, request: v1, response: script.Response{Status: 503, Header: http.Header{"Retry-After": {"5"}, "X-Served-By": {"go"}}}, expected: Some(rules[5])},
		"HeaderJoined":    {rules: rules, request: v1, response: script.Response{Status: 503, Header: http.Header{"Retry-After": {"5"}, "X-Served-By": {"rails", "go"}}}, expected: Some(rules[5])},
		"HeaderFunction":  {rules: rules, request: v1, response: script.Response{Status: 200, Header: html}, expected: Some(rules[3])},
		"RequestHeaders":  {rules: rules[4:], request: v3(http.Header{"X-Synthetic": {"true"}}), response: script.Response{Status: 200}, expected: Some(rules[4])},
		"RequestValue":    {rules: rules[4:5], request: v3(http.Header{"X-Synthetic": {"false"}}), response: script.Response{Status: 200}, expected: None[script.Rule]()},
		"RequestFunction": {rules: rules[4:5], request: v3(http.Header{"X-Synthetic": {"true"}, "Accept": {"*/*"}}), response: script.Response{Status: 200}, expected: None[script.Rule]()},
		"RequestRejected": {rules: rules, request: v1, response: script.Response{Status: 404}, expected: Some(rules[5])},
		"StatusRejected":  {rules: rules[:2], request: v2, response: script.Response{Status: 200}, expected: None[script.Rule]()},
		"NoRules":         {request: script.Request{Method: "GET", Path: "/"}, response: script.Response{Status: 200}, expected: None[script.Rule]()},
	} {
		t.Run(name, func(t *testing.T) {
			selected, err := script.SelectRule(t.Context(), scripts, handle, test.rules, test.request, test.response)
			assert.NoError(t, err)
			assert.Equal(t, test.expected, selected)
		})
	}
}

func TestNormalisesResponsesWithRules(t *testing.T) {
	scripts, handle := loadScripts(t, `
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
	rules := handle.Declared(scripts.Program()).Rules
	response, err := scripts.Schema().Type("test.v1.Response")
	assert.NoError(t, err)
	text := schema.StringType{}
	body := typed(t, response, `{"stable":"same","ignored":"first","roles":[],"users":[],"count":"0","labels":{}}`)
	for name, test := range map[string]struct {
		rule    script.Rule
		body    Option[schema.Value]
		status  Option[int]
		result  Option[schema.Value]
		message string
	}{
		"Typed":      {rule: rules[0], body: Some(body), status: Some(200), result: Some(typed(t, response, `{"stable":"same","roles":[],"users":[],"count":"0","labels":{}}`))},
		"Text":       {rule: rules[1], body: Some(typed(t, text, `" page "`)), status: None[int](), result: Some(typed(t, text, `"page"`))},
		"NoBody":     {rule: rules[1], body: None[schema.Value](), status: None[int](), result: None[schema.Value]()},
		"Unchanged":  {rule: rules[2], body: Some(body), status: Some(201), result: Some(body)},
		"NotObject":  {rule: rules[3], body: Some(body), message: "response normaliser must return an object"},
		"Member":     {rule: rules[4], body: Some(body), message: `normalised response has an unknown member "headers"`},
		"Status":     {rule: rules[5], body: Some(body), message: "normalised status must be an integer from 100 to 599"},
		"WrongBody":  {rule: rules[6], body: Some(body), message: "validate normalised body"},
		"TextNumber": {rule: rules[7], body: Some(typed(t, text, `"page"`)), message: "validate normalised body: $: expected string, found a number"},
	} {
		t.Run(name, func(t *testing.T) {
			status, result, err := script.NormaliseResponse(t.Context(), scripts, handle, test.rule, 201, test.body)
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

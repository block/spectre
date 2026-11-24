package script_test

import (
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/http/script"
	"github.com/block/spectre/internal/schema"
)

func TestDeclaresRulesAndEndpoints(t *testing.T) {
	program, handle, err := compile(t, fstest.MapFS{
		"weather.ts": scriptFile(`
			spectre.ingress.http("GET /v1/forecast").match<v1.Response>();
			spectre.ingress.http("GET /v2/forecast").match<v1.Response>();
			spectre.egress.http("GET weather.example/v1/forecast").match<v1.Response>();
		`),
	})
	assert.NoError(t, err)

	typed := script.ActionMatch{Type: objectType(t, program, "test.v1.Response")}
	assert.Equal(t, []rule{{`http("GET /v1/forecast")`, typed}, {`http("GET /v2/forecast")`, typed}}, rules(program, handle))
	assert.Equal(t, []endpoint{{script.Protocol, "GET weather.example/v1/forecast", "test.v1.Response"}}, endpoints(program, model.Egress))
	assert.Equal(t, []endpoint{}, endpoints(program, model.Ingress))
}

func TestDeclaresConnectError(t *testing.T) {
	program, _, err := newProgram(t, ``)
	assert.NoError(t, err)

	declared, err := program.Schema().Type(script.ConnectError)
	assert.NoError(t, err)
	assert.Equal(t, schema.TypeRef(script.ConnectError), declared.Name)
}

func TestDeclaresOptionalEgressEndpoints(t *testing.T) {
	program, _, err := compile(t, fstest.MapFS{
		"weather.ts": scriptFile(`
			spectre.egress.http("GET weather.example/v1/forecast").match<v1.Response>();
			spectre.egress.http("GET weather.example/v1/users").optional().match<v1.User>();
			spectre.egress.http("GET weather.example/v1/user").optional().replaces<v1.User, v1.User>("GET weather.example/v1/users").using<v1.User, v1.User>(
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

func TestRegistersRulesInOrder(t *testing.T) {
	program, handle, err := newProgram(t, `
		spectre.ingress.http("GET /_status").ignore();
		spectre.ingress.http("/").status(500, 503).match<spectre.Text>(({ status }) => ({ status }));
		spectre.ingress.http((request) => request.path.startsWith("/v2/")).status((status) => status >= 400).ignore();
		const forecast = spectre.ingress.http("GET /v1/forecast");
		forecast.status(404).match<v1.User>();
		forecast.match<v1.Response>();
		spectre.ingress.http("GET /v1/anything").match<unknown>();
		spectre.ingress.http("/").responseHeader("x-served-by", "rails").requestHeader("x-synthetic", "true").status(502).responseHeader("Retry-After", (value) => value !== undefined).ignore();
	`)
	assert.NoError(t, err)

	assert.Equal(t, []rule{
		{`http("GET /_status")`, script.ActionIgnore{}},
		{`http("/").status(500, 503)`, script.ActionMatch{Type: schema.StringType{}, Normalises: true}},
		{`http(<function>).status(<function>)`, script.ActionIgnore{}},
		{`http("GET /v1/forecast").status(404)`, script.ActionMatch{Type: objectType(t, program, "test.v1.User")}},
		{`http("GET /v1/forecast")`, script.ActionMatch{Type: objectType(t, program, "test.v1.Response")}},
		{`http("GET /v1/anything")`, script.ActionMatch{Type: schema.UnknownType{}}},
		{`http("/").requestHeader("X-Synthetic", "true").status(502).responseHeader("Retry-After", <function>).responseHeader("X-Served-By", "rails")`, script.ActionIgnore{}},
	}, rules(program, handle))
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	evaluator.Close()
}

func TestEvaluatesRuleFunctions(t *testing.T) {
	program, handle, err := newProgram(t, `
		spectre.ingress.http((request) => request.method === "GET" && request.path.startsWith("/v2/"))
			.status((status) => status >= 500)
			.match<v1.Response>(({ status, body }) => ({ status: status === 503 ? 500 : status, body }));
		spectre.ingress.http("/").status(() => "yes" as unknown as boolean).ignore();
		spectre.ingress.http(() => { throw new Error("no"); }).ignore();
		spectre.ingress.http("/").responseHeader("Content-Type", (type) => type === undefined || type.startsWith("text/")).ignore();
		spectre.ingress.http((request) => request.headers["x-synthetic"] === "a, b").requestHeader("Accept", (value) => value === "text/html").ignore();
	`)
	assert.NoError(t, err)
	declared := handle.Declared(program).Rules
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	callbacks := handle.Instance(evaluator)

	for name, test := range map[string]struct {
		request  script.Request
		expected bool
	}{
		"Selected":    {request: script.Request{Method: "GET", Path: "/v2/forecast"}, expected: true},
		"OtherMethod": {request: script.Request{Method: "POST", Path: "/v2/forecast"}},
		"OtherPath":   {request: script.Request{Method: "GET", Path: "/v1/forecast"}},
	} {
		t.Run(name, func(t *testing.T) {
			selected, err := callbacks.SelectsRequest(declared[0], test.request)
			assert.NoError(t, err)
			assert.Equal(t, test.expected, selected)
		})
	}
	selected, err := callbacks.SelectsStatus(declared[0], 503)
	assert.NoError(t, err)
	assert.True(t, selected)
	selected, err = callbacks.SelectsStatus(declared[0], 404)
	assert.NoError(t, err)
	assert.False(t, selected)

	normalised, err := callbacks.NormaliseResponse(declared[0], unknownValue(t, `{"status":503,"body":{"value":"a"}}`))
	assert.NoError(t, err)
	assert.Equal(t, Some(unknownValue(t, `{"status":500,"body":{"value":"a"}}`)), normalised)

	_, err = callbacks.SelectsStatus(declared[1], 500)
	assert.EqualError(t, err, `JavaScript selector of rule http("/").status(<function>) returned a value that is not a boolean`)
	_, err = callbacks.SelectsRequest(declared[2], script.Request{Method: "GET", Path: "/"})
	assert.EqualError(t, err, `JavaScript selector of rule http(<function>) threw an exception`)
	_, err = callbacks.SelectsStatus(declared[2], 500)
	assert.EqualError(t, err, `JavaScript status function of rule http(<function>) is unavailable`)

	for value, expected := range map[Option[string]]bool{Some("text/html"): true, Some("application/json"): false, None[string](): true} {
		selected, err = callbacks.SelectsResponseHeader(declared[3], "Content-Type", value)
		assert.NoError(t, err)
		assert.Equal(t, expected, selected)
	}
	selected, err = callbacks.SelectsRequest(declared[4], script.Request{Method: "GET", Path: "/", Header: http.Header{"X-Synthetic": {"a", "b"}}})
	assert.NoError(t, err)
	assert.True(t, selected)
	selected, err = callbacks.SelectsRequest(declared[4], script.Request{Method: "GET", Path: "/"})
	assert.NoError(t, err)
	assert.False(t, selected)
	selected, err = callbacks.SelectsRequestHeader(declared[4], "Accept", Some("text/html"))
	assert.NoError(t, err)
	assert.True(t, selected)
	_, err = callbacks.SelectsResponseHeader(declared[4], "Accept", Some("text/html"))
	assert.EqualError(t, err, `JavaScript function for response header "Accept" of rule http(<function>).requestHeader("Accept", <function>) is unavailable`)
	_, err = callbacks.SelectsResponseHeader(declared[3], "Accept", None[string]())
	assert.EqualError(t, err, `JavaScript function for response header "Accept" of rule http("/").responseHeader("Content-Type", <function>) is unavailable`)
}

func TestRejectsUncheckedRegistrations(t *testing.T) {
	for name, test := range map[string]struct {
		files   fstest.MapFS
		message string
	}{
		"MissingTypeArgument": {
			files:   fstest.MapFS{"test.ts": scriptFile(`spectre.ingress.http("GET /v1/forecast").match();`)},
			message: "scripts/test.ts:1:72: spectre.IngressHTTPRule.match needs 1 explicit type argument(s)",
		},
		"IgnoreTypeArgument": {
			files:   fstest.MapFS{"test.ts": scriptFile(`spectre.ingress.http("GET /_status").ignore<v1.Response>();`)},
			message: "Expected 0 type arguments, but got 1.",
		},
		"TextEgress": {
			files:   fstest.MapFS{"test.ts": scriptFile(`spectre.egress.http("GET weather.example/v1/forecast").match<spectre.Text>();`)},
			message: "only ingress rules may compare bodies as Text",
		},
		"UnknownEgress": {
			files:   fstest.MapFS{"test.ts": scriptFile(`spectre.egress.http("GET weather.example/v1/forecast").match<unknown>();`)},
			message: "error TS2344",
		},
		"ScalarBody": {
			files:   fstest.MapFS{"test.ts": scriptFile(`spectre.ingress.http("GET /root").match<string>();`)},
			message: "spectre.IngressHTTPRule.match type argument string: object types must be declared by name",
		},
		"NormaliserReturnsWrongBody": {
			files:   fstest.MapFS{"test.ts": scriptFile(`spectre.ingress.http("/").match<v1.Response>(() => ({ body: { value: 1 } }));`)},
			message: "is not assignable to parameter of type '(response: Response<Response>) => Response<Response>'",
		},
		"DuplicateScriptTypeName": {
			files: fstest.MapFS{
				"a.ts": scriptFile(`export interface Same { value: string } spectre.egress.http("GET a.example/x").match<Same>();`),
				"b.ts": scriptFile(`interface Same { value: string } spectre.egress.http("GET b.example/y").match<Same>();`),
			},
			message: `types declared at scripts/a.ts:1:`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := compile(t, test.files)
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
		"OptionalWithArgs": {body: `egress.http("GET weather.example/v1/forecast").optional(true);`, message: "spectre.EgressHTTPRule.optional takes no arguments"},
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
			_, _, err := newProgram(t, untyped+test.body)
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
			_, _, err := newProgram(t, untyped+test.body)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestAcceptsRulesThatDifferOnlyByFunction(t *testing.T) {
	_, _, err := newProgram(t, untyped+`
		ingress.http("/").status(500).ignore();
		ingress.http("/").status(500, 503).ignore();
		ingress.http("/").status(() => true).ignore();
		ingress.http("/").status(() => false).ignore();
		ingress.http(() => true).ignore();
		ingress.http(() => false).ignore();
		ingress.http("/").responseHeader("Accept", "a").ignore();
		ingress.http("/").responseHeader("Accept", "b").ignore();
		ingress.http("/").responseHeader("Accept", "a").responseHeader("Vary", "c").ignore();
		ingress.http("/").responseHeader("Accept", () => true).ignore();
		ingress.http("/").responseHeader("Accept", () => false).ignore();
		ingress.http("/").requestHeader("Accept", "a").ignore();
		ingress.http("/").requestHeader("Accept", "a").responseHeader("Accept", "a").ignore();
	`)
	assert.NoError(t, err)
}

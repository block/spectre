package javascript_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

const replacementScript = `
	spectre.egress.http("POST users.example/v1/users").match<v1.User>();
	spectre.egress.http("POST users.example/v1/user").replaces<v1.User, v1.Response>("POST users.example/v1/users").using<v1.User, v1.Response>(
		(request) => ({ name: request.name.toLowerCase() }),
		(request, reference) => ({ value: request.name, values: reference.values }),
	);
`

func TestRegistersReplacements(t *testing.T) {
	program, err := newProgram(t, replacementScript)
	assert.NoError(t, err)

	assert.Equal(t, []endpoint{
		{"http", "POST users.example/v1/users", "test.v1.User"},
		{"http", "POST users.example/v1/user", "test.v1.User"},
	}, endpoints(program, model.Egress))
	assert.Equal(t, []endpoint{}, endpoints(program, model.Ingress))
	replacements := program.Replacements()
	assert.Equal(t, 1, len(replacements))
	replacement := replacements[0]
	assert.Equal(t, `http("POST users.example/v1/user").replaces("POST users.example/v1/users").using()`, replacement.String())
	assert.Equal(t, model.ProtocolHTTP, replacement.Protocol())
	assert.Equal(t, "POST users.example/v1/user", replacement.Pattern())
	assert.Equal(t, "POST users.example/v1/users", replacement.Source())
	assert.Equal(t, []schema.TypeRef{"test.v1.Response", "test.v1.User", "test.v1.Response"},
		[]schema.TypeRef{replacement.Response(), replacement.SourceRequest(), replacement.SourceResponse()})
}

func TestEvaluatesReplacementFunctions(t *testing.T) {
	program, err := newProgram(t, replacementScript)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	replacement := program.Replacements()[0]

	ada := objectValue(t, program, "test.v1.User", `{"name":"Ada"}`)
	request, err := evaluator.ReplaceRequest(replacement, ada)
	assert.NoError(t, err)
	response, err := evaluator.ReplaceResponse(replacement, ada, objectValue(t, program, "test.v1.Response", `{"value":"x","values":["a","b"]}`))
	assert.NoError(t, err)

	assert.Equal(t, unknownValue(t, `{"name":"ada"}`), request)
	assert.Equal(t, unknownValue(t, `{"value":"Ada","values":["a","b"]}`), response)
}

func TestReplacementFunctionFailures(t *testing.T) {
	for name, test := range map[string]struct {
		request  string
		response string
		message  string
	}{
		"Throws": {
			request: `() => { throw new Error("boom"); }`, response: `() => ({ value: "", values: [] })`,
			message: `JavaScript request function of http("POST users.example/v1/user").replaces("POST users.example/v1/users").using() threw an exception`,
		},
		"Undefined": {
			request: `() => ({ name: "" })`, response: `() => undefined as unknown as v1.Response`,
			message: "JavaScript response function of http(\"POST users.example/v1/user\").replaces(\"POST users.example/v1/users\").using() returned undefined",
		},
	} {
		t.Run(name, func(t *testing.T) {
			program, err := newProgram(t, `
				spectre.egress.http("POST users.example/v1/users").match<v1.User>();
				spectre.egress.http("POST users.example/v1/user").replaces<v1.User, v1.Response>("POST users.example/v1/users").using<v1.User, v1.Response>(
					`+test.request+`, `+test.response+`);
			`)
			assert.NoError(t, err)
			evaluator, err := program.NewEvaluator(t.Context())
			assert.NoError(t, err)
			defer evaluator.Close()
			replacement := program.Replacements()[0]

			ada := objectValue(t, program, "test.v1.User", `{"name":"Ada"}`)
			_, requestErr := evaluator.ReplaceRequest(replacement, ada)
			_, responseErr := evaluator.ReplaceResponse(replacement, ada, objectValue(t, program, "test.v1.Response", `{"value":"","values":[]}`))

			err = requestErr
			if err == nil {
				err = responseErr
			}
			assert.EqualError(t, err, test.message)
		})
	}
}

func TestRejectsInvalidReplacements(t *testing.T) {
	const (
		rule        = `spectre.egress.http("POST users.example/v1/user")`
		replaces    = rule + `.replaces<v1.User, v1.Response>("POST users.example/v1/users")`
		functions   = `(request) => request, () => ({ value: "", values: [] })`
		untypedRule = `egress.http("POST users.example/v1/user").replaces("test.v1.User", "test.v1.Response", "POST users.example/v1/users")`
	)
	for name, test := range map[string]struct {
		body    string
		typed   bool
		message string
	}{
		"ReplacesOneTypeArgument": {
			body:    rule + `.replaces<v1.User>("POST users.example/v1/users").using<v1.User, v1.Response>(` + functions + `);`,
			typed:   true,
			message: "Expected 2 type arguments, but got 1.",
		},
		"UsingNoTypeArguments": {
			body:    replaces + `.using(() => ({ name: "" }), () => ({ value: "", values: [] }));`,
			typed:   true,
			message: "spectre.EgressReplacement.using needs 2 explicit type argument(s)",
		},
		"TextSourceResponse": {
			body:    rule + `.replaces<v1.User, spectre.Text>("POST users.example/v1/users");`,
			typed:   true,
			message: "only ingress rules may compare bodies as Text",
		},
		"ResponseFunctionReturnsWrongType": {
			body:    replaces + `.using<v1.User, v1.Response>((request) => request, () => ({ name: "" }));`,
			typed:   true,
			message: "is missing the following properties from type 'Response'",
		},
		"ReplacesArguments": {
			body:    `egress.http("POST users.example/v1/user").replaces("test.v1.User", "test.v1.Response");`,
			message: "spectre.EgressHTTPRule.replaces requires a source pattern",
		},
		"UsingArguments": {
			body:    untypedRule + `.using("test.v1.User", "test.v1.Response");`,
			message: "spectre.EgressReplacement.using requires a request function and a response function",
		},
		"RequestNotFunction": {
			body:    untypedRule + `.using("test.v1.User", "test.v1.Response", 1, () => ({}));`,
			message: "spectre.EgressReplacement.using request must be a function",
		},
		"ResponseNotFunction": {
			body:    untypedRule + `.using("test.v1.User", "test.v1.Response", () => ({}), 1);`,
			message: "spectre.EgressReplacement.using response must be a function",
		},
		"DuplicateEndpoint": {
			body:    `egress.http("POST users.example/v1/user").match("test.v1.User"); ` + untypedRule + `.using("test.v1.User", "test.v1.Response", () => ({}), () => ({}));`,
			message: `duplicate endpoint "POST users.example/v1/user"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := test.body
			if !test.typed {
				body = untyped + body
			}
			_, err := newProgram(t, body)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

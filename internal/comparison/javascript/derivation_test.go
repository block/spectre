package javascript_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

const derivationScript = `
	spectre.egress.http("POST users.example/v1/users").match<v1.User>();
	spectre.egress.http("POST users.example/v1/user").derive<v1.User, v1.Response>().from<v1.User, v1.Response>(
		"POST users.example/v1/users",
		(request) => ({ name: request.name.toLowerCase() }),
		(request, reference) => ({ value: request.name, values: reference.values }),
	);
`

func TestRegistersDerivations(t *testing.T) {
	program, err := newProgram(t, derivationScript)
	assert.NoError(t, err)

	assert.Equal(t, []endpoint{
		{"http", "POST users.example/v1/users", "test.v1.User"},
		{"http", "POST users.example/v1/user", "test.v1.User"},
	}, endpoints(program, model.Egress))
	assert.Equal(t, []endpoint{}, endpoints(program, model.Ingress))
	derivations := program.Derivations()
	assert.Equal(t, 1, len(derivations))
	derivation := derivations[0]
	assert.Equal(t, `http("POST users.example/v1/user").derive().from("POST users.example/v1/users")`, derivation.String())
	assert.Equal(t, model.ProtocolHTTP, derivation.Protocol())
	assert.Equal(t, "POST users.example/v1/user", derivation.Pattern())
	assert.Equal(t, "POST users.example/v1/users", derivation.Source())
	assert.Equal(t, []schema.TypeRef{"test.v1.Response", "test.v1.User", "test.v1.Response"},
		[]schema.TypeRef{derivation.Response(), derivation.SourceRequest(), derivation.SourceResponse()})
}

func TestEvaluatesDerivationFunctions(t *testing.T) {
	program, err := newProgram(t, derivationScript)
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	derivation := program.Derivations()[0]

	request, err := evaluator.DeriveRequest(derivation, map[string]any{"name": "Ada"})
	assert.NoError(t, err)
	response, err := evaluator.DeriveResponse(derivation, map[string]any{"name": "Ada"}, map[string]any{"value": "x", "values": []any{"a", "b"}})
	assert.NoError(t, err)

	assert.Equal(t, any(map[string]any{"name": "ada"}), request)
	assert.Equal(t, any(map[string]any{"value": "Ada", "values": []any{"a", "b"}}), response)
}

func TestDerivationFunctionFailures(t *testing.T) {
	for name, test := range map[string]struct {
		request  string
		response string
		message  string
	}{
		"Throws": {
			request: `() => { throw new Error("boom"); }`, response: `() => ({ value: "", values: [] })`,
			message: `JavaScript request function of http("POST users.example/v1/user").derive().from("POST users.example/v1/users") threw an exception`,
		},
		"Undefined": {
			request: `() => ({ name: "" })`, response: `() => undefined as unknown as v1.Response`,
			message: "JavaScript response function of http(\"POST users.example/v1/user\").derive().from(\"POST users.example/v1/users\") returned undefined",
		},
	} {
		t.Run(name, func(t *testing.T) {
			program, err := newProgram(t, `
				spectre.egress.http("POST users.example/v1/users").match<v1.User>();
				spectre.egress.http("POST users.example/v1/user").derive<v1.User, v1.Response>().from<v1.User, v1.Response>(
					"POST users.example/v1/users", `+test.request+`, `+test.response+`);
			`)
			assert.NoError(t, err)
			evaluator, err := program.NewEvaluator(t.Context())
			assert.NoError(t, err)
			defer evaluator.Close()
			derivation := program.Derivations()[0]

			_, requestErr := evaluator.DeriveRequest(derivation, map[string]any{"name": "Ada"})
			_, responseErr := evaluator.DeriveResponse(derivation, map[string]any{"name": "Ada"}, map[string]any{})

			err = requestErr
			if err == nil {
				err = responseErr
			}
			assert.EqualError(t, err, test.message)
		})
	}
}

func TestRejectsInvalidDerivations(t *testing.T) {
	const from = `"POST users.example/v1/users", (request) => request, () => ({ value: "", values: [] })`
	for name, test := range map[string]struct {
		body    string
		typed   bool
		message string
	}{
		"DeriveOneTypeArgument": {
			body:    `spectre.egress.http("POST users.example/v1/user").derive<v1.User>().from<v1.User, v1.Response>(` + from + `);`,
			typed:   true,
			message: "Expected 2 type arguments, but got 1.",
		},
		"FromNoTypeArguments": {
			body:    `spectre.egress.http("POST users.example/v1/user").derive<v1.User, v1.Response>().from(` + from + `);`,
			typed:   true,
			message: "spectre.EgressDerivation.from needs 2 explicit type argument(s)",
		},
		"TextResponse": {
			body:    `spectre.egress.http("POST users.example/v1/user").derive<v1.User, spectre.Text>();`,
			typed:   true,
			message: "only ingress rules may compare bodies as Text",
		},
		"ResponseFunctionReturnsWrongType": {
			body:    `spectre.egress.http("POST users.example/v1/user").derive<v1.User, v1.Response>().from<v1.User, v1.Response>("POST users.example/v1/users", (request) => request, () => ({ name: "" }));`,
			typed:   true,
			message: "is missing the following properties from type 'Response'",
		},
		"DeriveTypeNames": {
			body:    `egress.http("POST users.example/v1/user").derive("test.v1.User");`,
			message: "spectre.EgressHTTPRule.derive requires request and response type names",
		},
		"FromArguments": {
			body:    `egress.http("POST users.example/v1/user").derive("test.v1.User", "test.v1.Response").from("test.v1.User", "test.v1.Response", "POST users.example/v1/users");`,
			message: "spectre.EgressDerivation.from requires a source pattern, a request function, and a response function",
		},
		"RequestNotFunction": {
			body:    `egress.http("POST users.example/v1/user").derive("test.v1.User", "test.v1.Response").from("test.v1.User", "test.v1.Response", "POST users.example/v1/users", 1, () => ({}));`,
			message: "spectre.EgressDerivation.from request must be a function",
		},
		"ResponseNotFunction": {
			body:    `egress.http("POST users.example/v1/user").derive("test.v1.User", "test.v1.Response").from("test.v1.User", "test.v1.Response", "POST users.example/v1/users", () => ({}), 1);`,
			message: "spectre.EgressDerivation.from response must be a function",
		},
		"DuplicateEndpoint": {
			body:    `egress.http("POST users.example/v1/user").match("test.v1.User"); egress.http("POST users.example/v1/user").derive("test.v1.User", "test.v1.Response").from("test.v1.User", "test.v1.Response", "POST users.example/v1/users", () => ({}), () => ({}));`,
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

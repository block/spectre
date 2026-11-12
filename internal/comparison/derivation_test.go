package comparison_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/comparison"
)

// usersSource is a source endpoint for derivations of user lookups.
const usersSource = `spectre.egress.http("POST users.example/v1/users").match<v1.Query>();`

func TestDerivesValidatedRequestsAndResponses(t *testing.T) {
	scripts := newScripts(t, usersSource+`
		spectre.egress.http("POST users.example/v1/user").derive<v1.User, v1.User>().from<v1.Query, v1.Response>(
			"POST users.example/v1/users",
			(request) => ({ location: request.name, tags: [] }),
			(request, reference) => reference.users.find((user) => user.name === request.name) ?? { name: "" },
		);
	`)
	derivation := scripts.Derivations()[0]

	request, err := scripts.DeriveRequest(t.Context(), derivation, map[string]any{"name": "ada"})
	assert.NoError(t, err)
	response, err := scripts.DeriveResponse(t.Context(), derivation, map[string]any{"name": "ada"},
		decode(t, `{"stable": "", "roles": [], "users": [{"name": "bob"}, {"name": "ada"}], "count": "2", "labels": {}}`))
	assert.NoError(t, err)

	assert.Equal(t, any(map[string]any{"location": "ada", "tags": []any{}}), request)
	assert.Equal(t, any(map[string]any{"name": "ada"}), response)
}

func TestRejectsInvalidDerivedValues(t *testing.T) {
	// The casts skip type checking, so the results reach schema validation.
	scripts := newScripts(t, usersSource+`
		spectre.egress.http("POST users.example/v1/user").derive<v1.User, v1.User>().from<v1.Query, v1.Response>(
			"POST users.example/v1/users",
			() => ({ days: "many" }) as any,
			() => ({}) as any,
		);
	`)
	derivation := scripts.Derivations()[0]

	_, requestErr := scripts.DeriveRequest(t.Context(), derivation, map[string]any{"name": "ada"})
	_, responseErr := scripts.DeriveResponse(t.Context(), derivation, map[string]any{"name": "ada"}, map[string]any{})

	assert.Error(t, requestErr)
	assert.Contains(t, requestErr.Error(), "validate derived test.v1.Query")
	assert.Error(t, responseErr)
	assert.Contains(t, responseErr.Error(), "validate derived test.v1.User")
}

func TestRejectsInvalidDerivationSources(t *testing.T) {
	const derive = `spectre.egress.http("POST users.example/v1/user").derive<v1.User, v1.User>()`
	const functions = `(request) => ({ location: request.name, tags: [] }), () => ({ name: "" })`
	for name, test := range map[string]struct {
		script  string
		message string
	}{
		"Undeclared": {
			script:  derive + `.from<v1.Query, v1.Response>("POST users.example/v1/users", ` + functions + `);`,
			message: `http("POST users.example/v1/user").derive().from("POST users.example/v1/users"): source endpoint is not declared`,
		},
		"OtherRequestType": {
			script:  `spectre.egress.http("POST users.example/v1/users").match<v1.Request>();` + derive + `.from<v1.Query, v1.Response>("POST users.example/v1/users", ` + functions + `);`,
			message: "source requests are test.v1.Request, not test.v1.Query",
		},
		"DerivedSource": {
			script: derive + `.from<v1.Query, v1.Response>("POST users.example/v1/users", ` + functions + `);
				spectre.egress.http("POST users.example/v1/users").derive<v1.Query, v1.Response>().from<v1.User, v1.User>(
					"POST users.example/v1/user", () => ({ name: "" }), () => ({ stable: "", roles: [], users: [], count: "0", labels: {} }));`,
			message: "source endpoint is itself derived",
		},
		"OtherResponseType": {
			script: usersSource + derive + `.from<v1.Query, v1.Response>("POST users.example/v1/users", ` + functions + `);
				spectre.egress.http("POST users.example/v1/other").derive<v1.User, v1.User>().from<v1.Query, v1.User>(
					"POST users.example/v1/users", (request) => ({ location: request.name, tags: [] }), (_, reference) => reference);`,
			message: "source responses are decoded as both test.v1.Response and test.v1.User",
		},
	} {
		t.Run(name, func(t *testing.T) {
			scripts, err := comparison.New(t.Context(), newConfig(t, map[string]string{"test.ts": module(test.script)}))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
			assert.Equal(t, (*comparison.Scripts)(nil), scripts)
		})
	}
}

package comparison_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/schema"
)

const user schema.TypeRef = "test.v1.User"

// usersSource is a source endpoint replaced by user lookups.
const usersSource = `spectre.egress.http("POST users.example/v1/users").match<v1.Query>();`

func TestReplacesWithValidatedRequestsAndResponses(t *testing.T) {
	scripts := newScripts(t, usersSource+`
		spectre.egress.http("POST users.example/v1/user").replaces<v1.Query, v1.Response>("POST users.example/v1/users").using<v1.User, v1.User>(
			(request) => ({ location: request.name, tags: [] }),
			(request, reference) => reference.users?.find((user) => user.name === request.name) ?? { name: "" },
		);
	`)
	replacement := scripts.Replacements()[0]

	ada := instance(t, scripts, user, `{"name": "ada"}`)
	request, err := scripts.ReplaceRequest(t.Context(), replacement, ada)
	assert.NoError(t, err)
	translated, err := scripts.ReplaceResponse(t.Context(), replacement, ada,
		instance(t, scripts, response, `{"stable": "", "roles": [], "users": [{"name": "bob"}, {"name": "ada"}], "count": "2", "labels": {}}`))
	assert.NoError(t, err)

	assert.Equal(t, instance(t, scripts, query, `{"location": "ada", "tags": []}`), request)
	assert.Equal(t, ada, translated)
}

func TestRejectsInvalidTranslatedValues(t *testing.T) {
	// The casts skip type checking, so the results reach schema validation.
	scripts := newScripts(t, usersSource+`
		spectre.egress.http("POST users.example/v1/user").replaces<v1.Query, v1.Response>("POST users.example/v1/users").using<v1.User, v1.User>(
			() => ({ days: "many" }) as any,
			() => ({}) as any,
		);
	`)
	replacement := scripts.Replacements()[0]

	ada := instance(t, scripts, user, `{"name": "ada"}`)
	_, requestErr := scripts.ReplaceRequest(t.Context(), replacement, ada)
	_, responseErr := scripts.ReplaceResponse(t.Context(), replacement, ada, instance(t, scripts, response, `{}`))

	assert.Error(t, requestErr)
	assert.Contains(t, requestErr.Error(), "validate translated test.v1.Query")
	assert.Error(t, responseErr)
	assert.Contains(t, responseErr.Error(), "validate translated test.v1.User")
}

func TestRejectsInvalidReplacementSources(t *testing.T) {
	const rule = `spectre.egress.http("POST users.example/v1/user")`
	const using = `.using<v1.User, v1.User>((request) => ({ location: request.name, tags: [] }), () => ({ name: "" }));`
	for name, test := range map[string]struct {
		script  string
		message string
	}{
		"Undeclared": {
			script:  rule + `.replaces<v1.Query, v1.Response>("POST users.example/v1/users")` + using,
			message: `http("POST users.example/v1/user").replaces("POST users.example/v1/users").using(): source endpoint is not declared`,
		},
		"SourceWithoutHost": {
			script:  rule + `.replaces<v1.Query, v1.Response>("POST /v1/users")` + using,
			message: "source endpoint is not declared",
		},
		"SourceIsItself": {
			script:  rule + `.replaces<v1.User, v1.User>("POST users.example/v1/user").using<v1.User, v1.User>((request) => request, (_, reference) => reference);`,
			message: "source endpoint itself replaces another",
		},
		"OtherRequestType": {
			script:  `spectre.egress.http("POST users.example/v1/users").match<v1.Request>();` + rule + `.replaces<v1.Query, v1.Response>("POST users.example/v1/users")` + using,
			message: "source requests are test.v1.Request, not test.v1.Query",
		},
		"ReplacingSource": {
			script: rule + `.replaces<v1.Query, v1.Response>("POST users.example/v1/users")` + using + `
				spectre.egress.http("POST users.example/v1/users").replaces<v1.User, v1.User>("POST users.example/v1/user").using<v1.Query, v1.Response>(
					() => ({ name: "" }), () => ({ stable: "", roles: [], users: [], count: "0", labels: {} }));`,
			message: "source endpoint itself replaces another",
		},
		"OtherResponseType": {
			script: usersSource + rule + `.replaces<v1.Query, v1.Response>("POST users.example/v1/users")` + using + `
				spectre.egress.http("POST users.example/v1/other").replaces<v1.Query, v1.User>("POST users.example/v1/users").using<v1.User, v1.User>(
					(request) => ({ location: request.name, tags: [] }), (_, reference) => reference);`,
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

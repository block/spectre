package state_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/state"
)

func TestParse(t *testing.T) {
	for name, test := range map[string]struct {
		toml     string
		expected state.State
	}{
		"Empty": {},
		"Nodes": {
			toml: `
[nodes.money]
status = "migrated"

[nodes.api]
depends_on = ["money"]

[nodes.api.ingress.http]
routes = { "GET /a" = {}, "PUT /a" = {} }

[nodes.api.egress.http]
`,
			expected: state.State{Nodes: []state.Node{
				{
					ID: "api", Line: 5, DependsOn: []string{"money"}, Status: state.Pending,
					Ingress: state.Ingress{HTTP: &state.HTTPIngress{Routes: map[model.Route]state.HTTPRoute{"GET /a": {}, "PUT /a": {}}}},
					Egress:  state.Egress{HTTP: &state.HTTPEgress{}},
				},
				{ID: "money", Line: 2, Status: state.Migrated},
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			actual, err := state.Parse([]byte(test.toml))
			assert.NoError(t, err)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestNodeJSON(t *testing.T) {
	parsed, err := state.Parse([]byte(`
[nodes.api.ingress.http.routes]
"GET /<a>" = {}

[nodes.api.egress.http]

[nodes.money]
`))
	assert.NoError(t, err)
	var actual bytes.Buffer
	encoder := json.NewEncoder(&actual)
	encoder.SetEscapeHTML(false)
	assert.NoError(t, encoder.Encode(parsed.Nodes))
	assert.Equal(t, `[{"id":"api","status":"pending","ingress":{"http":{"routes":{"GET /<a>":{}}}},"egress":{"http":{}}},`+
		`{"id":"money","status":"pending"}]`+"\n", actual.String())
}

func TestParseProblems(t *testing.T) {
	for name, test := range map[string]struct {
		toml     string
		expected []state.Problem
	}{
		"Syntax": {
			toml:     "[nodes.a]\nstatus =\n",
			expected: []state.Problem{{Line: 2, Message: "unexpected character U+000A at start of value"}},
		},
		"UnknownKeys": {
			toml: "owner = \"me\"\n\n[nodes.a]\ncolour = \"red\"\n\n[nodes.a.ingress.kafka]\n\n[nodes.a.ingress.http]\nroutes = { \"GET /a\" = {} }\ntimeout = 1\n",
			expected: []state.Problem{
				{Line: 1, Message: "owner: unknown key"},
				{Line: 4, Message: "nodes.a.colour: unknown key"},
				{Line: 6, Message: "nodes.a.ingress.kafka: unknown key"},
				{Line: 10, Message: "nodes.a.ingress.http.timeout: unknown key"},
			},
		},
		"WrongType": {
			toml:     "[nodes.a]\ndepends_on = [1]\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a.depends_on: cannot decode TOML integer into struct field state.Node.DependsOn of type string"}},
		},
		"RoutesNotATable": {
			toml: "[nodes.a.ingress.http]\nroutes = [\"GET /a\"]\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a.ingress.http.routes: cannot decode TOML array into struct field " +
				"state.HTTPIngress.Routes of type map[model.Route]state.HTTPRoute"}},
		},
		"NodeID": {
			toml:     "[nodes.Money_Utils]\n",
			expected: []state.Problem{{Line: 1, Message: "nodes.Money_Utils: node IDs must be kebab-case"}},
		},
		"Status": {
			toml:     "[nodes.a]\nstatus = \"done\"\n",
			expected: []state.Problem{{Line: 2, Message: `nodes.a.status: must be "pending" or "migrated"`}},
		},
		"NoRoutes": {
			toml:     "[nodes.a.ingress.http.routes]\n",
			expected: []state.Problem{{Line: 1, Message: "nodes.a.ingress.http: routes must list at least one route"}},
		},
		"Dependencies": {
			toml: "[nodes.a]\ndepends_on = [\"a\", \"b\", \"c\", \"c\"]\nstatus = \"migrated\"\n\n[nodes.c]\n",
			expected: []state.Problem{
				{Line: 2, Message: "nodes.a.depends_on: a node cannot depend on itself"},
				{Line: 2, Message: `nodes.a.depends_on: "b" is not a node`},
				{Line: 2, Message: `nodes.a.depends_on: lists "c" twice`},
				{Line: 3, Message: `nodes.a.status: migrated, but depends on pending node "c"`},
			},
		},
		"Cycle": {
			toml: "[nodes.a]\ndepends_on = [\"b\"]\n\n[nodes.b]\ndepends_on = [\"c\"]\n\n[nodes.c]\ndepends_on = [\"b\"]\n",
			expected: []state.Problem{
				{Line: 5, Message: "nodes.b.depends_on: dependency cycle b -> c -> b"},
			},
		},
		"UniqueRoutes": {
			toml: "[nodes.a.ingress.http.routes]\n\"GET /a\" = {}\n\n[nodes.b.ingress.http.routes]\n\"GET /a\" = {}\n",
			expected: []state.Problem{
				{Line: 5, Message: `nodes.b.ingress.http.routes."GET /a": also listed by node "a"`},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := state.Parse([]byte(test.toml))
			invalid, ok := errors.AsType[*state.InvalidError](err)
			assert.True(t, ok, "expected an invalid state, got %v", err)
			assert.Equal(t, test.expected, invalid.Problems)
		})
	}
}

func TestInvalidErrorReportsFirstProblem(t *testing.T) {
	err := &state.InvalidError{Problems: []state.Problem{{Line: 3, Message: "a"}, {Message: "b"}, {Message: "c"}}}
	assert.EqualError(t, err, "line 3: a (and 2 more problems)")
}

func TestUnreached(t *testing.T) {
	parsed, err := state.Parse([]byte(`
[nodes.api]
depends_on = ["client"]
[nodes.api.ingress.http.routes]
"GET /api" = {}

[nodes.client]
depends_on = ["money"]
[nodes.client.egress.http]

[nodes.money]

[nodes.job]
depends_on = ["money"]
`))
	assert.NoError(t, err)
	assert.Equal(t, []state.Problem{{Line: 13, Message: "nodes.job: no ingress node reaches it, so SPECTRE cannot verify it"}}, parsed.Unreached())
}

func TestNext(t *testing.T) {
	for name, test := range map[string]struct {
		toml     string
		expected []string
	}{
		"AllMigrated": {
			toml: "[nodes.api]\nstatus = \"migrated\"\n[nodes.api.ingress.http.routes]\n\"GET /api\" = {}\n",
		},
		"FewestNodesWithIO": {
			toml: `
[nodes.money]

[nodes.ledger-client]
depends_on = ["money"]
[nodes.ledger-client.egress.http]

[nodes.invoices-api]
depends_on = ["money", "ledger-client"]
[nodes.invoices-api.ingress.http.routes]
"GET /invoices-api" = {}

[nodes.reports-api]
depends_on = ["money"]
[nodes.reports-api.ingress.http.routes]
"GET /reports-api" = {}
`,
			expected: []string{"money", "reports-api"},
		},
		"FewerNodesWithIOBeforeFewerNodes": {
			toml: `
[nodes.a-api]
depends_on = ["client"]
[nodes.a-api.ingress.http.routes]
"GET /a-api" = {}

[nodes.client.egress.http]

[nodes.b-api]
depends_on = ["one", "two"]
[nodes.b-api.ingress.http.routes]
"GET /b-api" = {}

[nodes.one]
[nodes.two]
`,
			expected: []string{"one", "two", "b-api"},
		},
		"FewestNodes": {
			toml: `
[nodes.a-api]
depends_on = ["one", "two"]
[nodes.a-api.ingress.http.routes]
"GET /a-api" = {}

[nodes.b-api]
depends_on = ["one"]
[nodes.b-api.ingress.http.routes]
"GET /b-api" = {}

[nodes.one]
[nodes.two]
`,
			expected: []string{"one", "b-api"},
		},
		"LowestIDOnTie": {
			toml: `
[nodes.b-api.ingress.http.routes]
"GET /b-api" = {}
[nodes.a-api.ingress.http.routes]
"GET /a-api" = {}
`,
			expected: []string{"a-api"},
		},
		"IngressDependency": {
			toml: `
[nodes.api]
depends_on = ["auth"]
[nodes.api.ingress.http.routes]
"GET /api" = {}

[nodes.auth.ingress.http.routes]
"GET /auth" = {}
`,
			expected: []string{"auth"},
		},
		"DependenciesFirst": {
			toml: `
[nodes.api]
depends_on = ["b", "a"]
[nodes.api.ingress.http.routes]
"GET /api" = {}

[nodes.a]
depends_on = ["c"]

[nodes.b]

[nodes.c]
status = "migrated"
`,
			expected: []string{"a", "b", "api"},
		},
		"OnlyUnreachedPending": {
			toml: `
[nodes.api]
status = "migrated"
[nodes.api.ingress.http.routes]
"GET /api" = {}

[nodes.job]
`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			parsed, err := state.Parse([]byte(test.toml))
			assert.NoError(t, err)
			var expected []state.Node
			for _, id := range test.expected {
				expected = append(expected, node(t, parsed, id))
			}
			assert.Equal(t, expected, parsed.Next())
		})
	}
}

func TestPending(t *testing.T) {
	parsed, err := state.Parse([]byte("[nodes.a]\n[nodes.b]\nstatus = \"migrated\"\n[nodes.c]\n"))
	assert.NoError(t, err)
	assert.Equal(t, []state.Node{node(t, parsed, "a"), node(t, parsed, "c")}, parsed.Pending())
}

func TestTypes(t *testing.T) {
	parsed, err := state.Parse([]byte("[nodes.api.egress.http]\n[nodes.api.ingress.http.routes]\n\"GET /api\" = {}\n[nodes.money]\n"))
	assert.NoError(t, err)
	assert.Equal(t, []string{"ingress[http]", "egress[http]"}, node(t, parsed, "api").Types())
	assert.Equal(t, []string{state.Library}, node(t, parsed, "money").Types())
}

func node(t *testing.T, parsed state.State, id string) state.Node {
	t.Helper()
	for _, node := range parsed.Nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("no node %q", id)
	return state.Node{}
}

func TestMarkMigrated(t *testing.T) {
	for name, test := range map[string]struct {
		toml     string
		ids      []string
		expected string
		marked   []string
	}{
		"Rewrites": {
			toml: "# money\n[nodes.b]\ndepends_on = [\"a\"]\nstatus = \"pending\"\n\n" +
				"[nodes.b.ingress.http.routes]\n\"GET /b\" = {}\n\n[nodes.b.egress.http]\n\n[nodes]\na = {}\nc = { status = 'pending' }\nd = {}\n",
			ids: []string{"b", "a", "b"},
			expected: "[nodes]\n[nodes.a]\nstatus = 'migrated'\n\n[nodes.b]\ndepends_on = ['a']\nstatus = 'migrated'\n\n" +
				"[nodes.b.ingress]\n[nodes.b.ingress.http]\n[nodes.b.ingress.http.routes]\n[nodes.b.ingress.http.routes.'GET /b']\n" +
				"[nodes.b.egress]\n[nodes.b.egress.http]\n[nodes.c]\nstatus = 'pending'\n\n[nodes.d]\nstatus = 'pending'\n",
			marked: []string{"b", "a"},
		},
		"AlreadyMigrated": {
			toml:     "# money\n[nodes.a]\nstatus = \"migrated\"\n",
			ids:      []string{"a"},
			expected: "# money\n[nodes.a]\nstatus = \"migrated\"\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			updated, marked, err := state.MarkMigrated([]byte(test.toml), test.ids)
			assert.NoError(t, err)
			assert.Equal(t, test.expected, string(updated))
			assert.Equal(t, test.marked, marked)
		})
	}
}

func TestMarkMigratedErrors(t *testing.T) {
	for name, test := range map[string]struct {
		toml     string
		ids      []string
		err      string
		problems []state.Problem
	}{
		"UnknownNodes": {
			toml: "[nodes.a]\n",
			ids:  []string{"b", "a", "c"},
			err:  "not a node: b, c",
		},
		"PendingDependency": {
			toml:     "[nodes.a]\ndepends_on = [\"b\"]\n\n[nodes.b]\n",
			ids:      []string{"a"},
			problems: []state.Problem{{Message: `nodes.a.status: migrated, but depends on pending node "b"`}},
		},
		"InvalidBefore": {
			toml:     "[nodes.a]\nstatus = \"done\"\n",
			ids:      []string{"a"},
			problems: []state.Problem{{Line: 2, Message: `nodes.a.status: must be "pending" or "migrated"`}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := state.MarkMigrated([]byte(test.toml), test.ids)
			if len(test.problems) == 0 {
				assert.EqualError(t, err, test.err)
				return
			}
			invalid, ok := errors.AsType[*state.InvalidError](err)
			assert.True(t, ok, "expected an invalid state, got %v", err)
			assert.Equal(t, test.problems, invalid.Problems)
		})
	}
}

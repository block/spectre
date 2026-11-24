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

// fakeProtocol is a protocol that the tests give kinds of their own.
const fakeProtocol model.Protocol = "fake"

var (
	ingressFake = state.Kind{
		Direction:   model.Ingress,
		Protocol:    fakeProtocol,
		Description: "Receives fake requests.",
		Settings:    []state.Setting{{Name: "routes", Required: true, Description: "Routes.", Key: "<METHOD> /<path>", Unique: true}},
	}
	egressFake = state.Kind{Direction: model.Egress, Protocol: fakeProtocol, Description: "Sends fake requests."}
	egressSQL  = state.Kind{
		Direction:   model.Egress,
		Protocol:    "sql",
		Description: "Sends SQL statements.",
		Settings:    []state.Setting{{Name: "tables", Description: "Tables.", Key: "<name>", Unique: true}},
	}
	// kinds are those of a protocol that has a unique setting, plus
	// a protocol that has a plain setting and a unique setting that is optional.
	kinds = supportedKinds(ingressFake, egressFake, egressSQL)
)

// supportedKinds returns Kinds that support each kind.
func supportedKinds(definitions ...state.Kind) state.Kinds {
	supported := state.NewKinds()
	for _, definition := range definitions {
		if err := supported.Add(definition); err != nil {
			panic(err)
		}
	}
	return supported
}

func TestParse(t *testing.T) {
	for name, test := range map[string]struct {
		toml     string
		expected []state.Node
	}{
		"Empty": {},
		"Nodes": {
			toml: `
[nodes.money]
status = "migrated"

[nodes.api]
depends_on = ["money"]

[nodes.api.ingress.fake]
routes = { "GET /a" = {}, "PUT /a" = {} }

[nodes.api.egress.fake]

[nodes.api.egress.sql.tables]
orders = {}
`,
			expected: []state.Node{
				{
					ID: "api", Line: 5, DependsOn: []string{"money"}, Status: state.Pending,
					Ingress: map[model.Protocol]state.Settings{fakeProtocol: {"routes": {"GET /a": {}, "PUT /a": {}}}},
					Egress: map[model.Protocol]state.Settings{
						fakeProtocol: {},
						"sql":        {"tables": {"orders": {}}},
					},
				},
				{ID: "money", Line: 2, Status: state.Migrated},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			actual, err := kinds.Parse([]byte(test.toml))
			assert.NoError(t, err)
			assert.Equal(t, test.expected, actual.All)
		})
	}
}

func TestNodeJSON(t *testing.T) {
	parsed, err := kinds.Parse([]byte(`
[nodes.api.ingress.fake.routes]
"GET /<a>" = {}

[nodes.api.egress.fake]

[nodes.money]
`))
	assert.NoError(t, err)
	var actual bytes.Buffer
	encoder := json.NewEncoder(&actual)
	encoder.SetEscapeHTML(false)
	assert.NoError(t, encoder.Encode(parsed.All))
	assert.Equal(t, `[{"id":"api","status":"pending","ingress":{"fake":{"routes":{"GET /<a>":{}}}},"egress":{"fake":{}}},`+
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
			toml: "owner = \"me\"\n\n[nodes.a]\ncolour = \"red\"\n",
			expected: []state.Problem{
				{Line: 1, Message: "owner: unknown key"},
				{Line: 4, Message: "nodes.a.colour: unknown key"},
			},
		},
		"UnsupportedKindsAndUndefinedSettings": {
			toml: "[nodes.a.ingress.kafka]\n\n[nodes.a.egress.fake.hosts]\n\n[nodes.a.ingress.fake]\nroutes = { \"GET /a\" = {} }\ntimeout = {}\n\n[nodes.a.ingress.sql]\n",
			expected: []state.Problem{
				{Line: 1, Message: "nodes.a.ingress.kafka: unknown key"},
				{Line: 3, Message: "nodes.a.egress.fake.hosts: unknown key"},
				{Line: 7, Message: "nodes.a.ingress.fake.timeout: unknown key"},
				{Line: 9, Message: "nodes.a.ingress.sql: unknown key"},
			},
		},
		"EntryHasSettings": {
			toml:     "[nodes.a.ingress.fake.routes]\n\"GET /a\" = { timeout = 1 }\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a.ingress.fake.routes.timeout: unknown key"}},
		},
		"WrongType": {
			toml:     "[nodes.a]\ndepends_on = [1]\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a.depends_on: cannot decode TOML integer into struct field state.Node.DependsOn of type string"}},
		},
		"RoutesNotATable": {
			toml:     "[nodes.a.ingress.fake]\nroutes = [\"GET /a\"]\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a.ingress.fake.routes: cannot decode TOML array into map[string]struct {}"}},
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
			toml:     "[nodes.a.ingress.fake.routes]\n",
			expected: []state.Problem{{Line: 1, Message: "nodes.a.ingress.fake: routes must list at least one entry"}},
		},
		"NoRoutesSetting": {
			toml:     "[nodes.a.ingress.fake]\n",
			expected: []state.Problem{{Line: 1, Message: "nodes.a.ingress.fake: routes must list at least one entry"}},
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
		"OptionalSettingIsNotRequired": {
			toml: "[nodes.a.egress.sql]\n[nodes.a.egress.sql.tables]\n",
		},
		"UniqueSettingAcrossNodes": {
			toml: "[nodes.a.egress.sql.tables]\norders = {}\n\n[nodes.b.egress.sql.tables]\norders = {}\n",
			expected: []state.Problem{
				{Line: 5, Message: `nodes.b.egress.sql.tables.orders: also listed by node "a"`},
			},
		},
		"UniqueRoutes": {
			toml: "[nodes.a.ingress.fake.routes]\n\"GET /a\" = {}\n\n[nodes.b.ingress.fake.routes]\n\"GET /a\" = {}\n",
			expected: []state.Problem{
				{Line: 5, Message: `nodes.b.ingress.fake.routes."GET /a": also listed by node "a"`},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := kinds.Parse([]byte(test.toml))
			if len(test.expected) == 0 {
				assert.NoError(t, err)
				return
			}
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
	parsed, err := kinds.Parse([]byte(`
[nodes.api]
depends_on = ["client"]
[nodes.api.ingress.fake.routes]
"GET /api" = {}

[nodes.client]
depends_on = ["money"]
[nodes.client.egress.fake]

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
			toml: "[nodes.api]\nstatus = \"migrated\"\n[nodes.api.ingress.fake.routes]\n\"GET /api\" = {}\n",
		},
		"FewestNodesWithIO": {
			toml: `
[nodes.money]

[nodes.ledger-client]
depends_on = ["money"]
[nodes.ledger-client.egress.fake]

[nodes.invoices-api]
depends_on = ["money", "ledger-client"]
[nodes.invoices-api.ingress.fake.routes]
"GET /invoices-api" = {}

[nodes.reports-api]
depends_on = ["money"]
[nodes.reports-api.ingress.fake.routes]
"GET /reports-api" = {}
`,
			expected: []string{"money", "reports-api"},
		},
		"FewerNodesWithIOBeforeFewerNodes": {
			toml: `
[nodes.a-api]
depends_on = ["client"]
[nodes.a-api.ingress.fake.routes]
"GET /a-api" = {}

[nodes.client.egress.fake]

[nodes.b-api]
depends_on = ["one", "two"]
[nodes.b-api.ingress.fake.routes]
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
[nodes.a-api.ingress.fake.routes]
"GET /a-api" = {}

[nodes.b-api]
depends_on = ["one"]
[nodes.b-api.ingress.fake.routes]
"GET /b-api" = {}

[nodes.one]
[nodes.two]
`,
			expected: []string{"one", "b-api"},
		},
		"LowestIDOnTie": {
			toml: `
[nodes.b-api.ingress.fake.routes]
"GET /b-api" = {}
[nodes.a-api.ingress.fake.routes]
"GET /a-api" = {}
`,
			expected: []string{"a-api"},
		},
		"IngressDependency": {
			toml: `
[nodes.api]
depends_on = ["auth"]
[nodes.api.ingress.fake.routes]
"GET /api" = {}

[nodes.auth.ingress.fake.routes]
"GET /auth" = {}
`,
			expected: []string{"auth"},
		},
		"DependenciesFirst": {
			toml: `
[nodes.api]
depends_on = ["b", "a"]
[nodes.api.ingress.fake.routes]
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
[nodes.api.ingress.fake.routes]
"GET /api" = {}

[nodes.job]
`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			parsed, err := kinds.Parse([]byte(test.toml))
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
	parsed, err := kinds.Parse([]byte("[nodes.a]\n[nodes.b]\nstatus = \"migrated\"\n[nodes.c]\n"))
	assert.NoError(t, err)
	assert.Equal(t, []state.Node{node(t, parsed, "a"), node(t, parsed, "c")}, parsed.Pending())
}

func TestTypes(t *testing.T) {
	parsed, err := kinds.Parse([]byte("[nodes.api.egress.sql]\n[nodes.api.egress.fake]\n[nodes.api.ingress.fake.routes]\n\"GET /api\" = {}\n[nodes.money]\n"))
	assert.NoError(t, err)
	assert.Equal(t, []string{"ingress[fake]", "egress[fake]", "egress[sql]"}, node(t, parsed, "api").Types())
	assert.Equal(t, []string{state.Library}, node(t, parsed, "money").Types())
}

const kindsToml = `
[nodes.api.ingress.fake.routes]
"GET /a" = {}

[nodes.api.egress.sql.tables]
orders = {}

[nodes.api.egress.fake]

[nodes.job]
status = "migrated"

[nodes.job.egress.sql.tables]
users = {}
`

func TestNodes(t *testing.T) {
	parsed, err := kinds.Parse([]byte(kindsToml))
	assert.NoError(t, err)
	assert.Equal(t, []state.KindNode{{ID: "api", Settings: state.Settings{"routes": {"GET /a": {}}}}}, parsed.Nodes(ingressFake))
	assert.Equal(t, []state.KindNode{{ID: "api", Settings: state.Settings{}}}, parsed.Nodes(egressFake))
	assert.Equal(t, []state.KindNode{
		{ID: "api", Settings: state.Settings{"tables": {"orders": {}}}},
		{ID: "job", Migrated: true, Settings: state.Settings{"tables": {"users": {}}}},
	}, parsed.Nodes(egressSQL))
	assert.Zero(t, parsed.Nodes(state.Kind{Direction: model.Ingress, Protocol: "sql"}))
}

func TestOwner(t *testing.T) {
	parsed, err := kinds.Parse([]byte(kindsToml))
	assert.NoError(t, err)
	for name, test := range map[string]struct {
		traffic model.Traffic
		node    string
		ok      bool
	}{
		"Route":            {traffic: traffic(ingressFake, "GET /a"), node: "api", ok: true},
		"SettingOfAnyNode": {traffic: traffic(egressSQL, "users"), node: "job", ok: true},
		"UnlistedRoute":    {traffic: traffic(ingressFake, "GET /b")},
		"WrongKind":        {traffic: traffic(egressSQL, "GET /a")},
		"NoUniqueSetting":  {traffic: traffic(egressFake, "GET /a")},
		"UnsupportedKind":  {traffic: traffic(state.Kind{Direction: model.Ingress, Protocol: "sql"}, "orders")},
	} {
		t.Run(name, func(t *testing.T) {
			node, ok := parsed.Owner(test.traffic)
			assert.Equal(t, test.node, node)
			assert.Equal(t, test.ok, ok)
		})
	}
}

// traffic is the traffic of kind on route.
func traffic(kind state.Kind, route model.Route) model.Traffic {
	return model.Traffic{Direction: kind.Direction, Protocol: kind.Protocol, Route: route}
}

func TestKindsRejectDuplicate(t *testing.T) {
	supported := state.NewKinds()
	assert.NoError(t, supported.Add(ingressFake))
	assert.NoError(t, supported.Add(egressFake))
	err := supported.Add(egressFake)
	assert.EqualError(t, err, "egress[fake] is defined more than once")
}

func TestNodeTypes(t *testing.T) {
	assert.Equal(t, []state.NodeType{
		{Name: state.Library, Description: "Module shared by more than one other node that performs no IO, " +
			"such as shared domain types, validation, calculations, and helpers."},
		{
			Name: "ingress[fake]", Description: "Receives fake requests.", Table: "nodes.<id>.ingress.fake",
			Settings: ingressFake.Settings,
		},
		{Name: "egress[fake]", Description: "Sends fake requests.", Table: "nodes.<id>.egress.fake"},
		{
			Name: "egress[sql]", Description: "Sends SQL statements.", Table: "nodes.<id>.egress.sql",
			Settings: egressSQL.Settings,
		},
	}, kinds.NodeTypes())
}

func node(t *testing.T, parsed state.State, id string) state.Node {
	t.Helper()
	for _, node := range parsed.All {
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
				"[nodes.b.ingress.fake.routes]\n\"GET /b\" = {}\n\n[nodes.b.egress.fake]\n\n[nodes]\na = {}\nc = { status = 'pending' }\nd = {}\n",
			ids: []string{"b", "a", "b"},
			expected: "[nodes]\n[nodes.a]\nstatus = 'migrated'\n\n[nodes.b]\ndepends_on = ['a']\nstatus = 'migrated'\n\n" +
				"[nodes.b.ingress]\n[nodes.b.ingress.fake]\n[nodes.b.ingress.fake.routes]\n[nodes.b.ingress.fake.routes.'GET /b']\n" +
				"[nodes.b.egress]\n[nodes.b.egress.fake]\n[nodes.c]\nstatus = 'pending'\n\n[nodes.d]\nstatus = 'pending'\n",
			marked: []string{"b", "a"},
		},
		"AlreadyMigrated": {
			toml:     "# money\n[nodes.a]\nstatus = \"migrated\"\n",
			ids:      []string{"a"},
			expected: "# money\n[nodes.a]\nstatus = \"migrated\"\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			updated, marked, err := kinds.MarkMigrated([]byte(test.toml), test.ids)
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
			_, _, err := kinds.MarkMigrated([]byte(test.toml), test.ids)
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

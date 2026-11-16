package state_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/state"
)

var (
	rpcIngress = model.IOKind{Direction: model.Ingress, Protocol: "rpc"}
	rpcEgress  = model.IOKind{Direction: model.Egress, Protocol: "rpc"}
)

func testCatalog(t *testing.T) *state.Catalog {
	t.Helper()
	catalog, err := state.NewCatalog(
		state.Support{Kind: rpcEgress},
		state.Support{Kind: rpcIngress, Settings: []state.Setting{
			{Name: "methods", Required: true, Key: "method name", Unique: true, Entry: []state.Setting{{Name: "timeout"}}},
			{Name: "group"},
		}},
	)
	assert.NoError(t, err)
	return catalog
}

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

[nodes.api.ingress.rpc]
group = "public"
methods = { Get = {}, Put = { timeout = "1s" } }

[nodes.api.egress.rpc]
`,
			expected: state.State{Nodes: []state.Node{
				{ID: "api", Line: 5, DependsOn: []string{"money"}, Status: state.Pending, IO: map[model.IOKind]map[string]any{
					rpcIngress: {"group": "public", "methods": map[string]any{"Get": map[string]any{}, "Put": map[string]any{"timeout": "1s"}}},
					rpcEgress:  {},
				}},
				{ID: "money", Line: 2, Status: state.Migrated, IO: map[model.IOKind]map[string]any{}},
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			actual, err := state.Parse([]byte(test.toml), testCatalog(t))
			assert.NoError(t, err)
			assert.Equal(t, test.expected, actual)
		})
	}
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
		"UnknownTopLevelKey": {
			toml:     "owner = \"me\"\n",
			expected: []state.Problem{{Line: 1, Message: "owner: unknown key; the state holds only nodes"}},
		},
		"NodesNotATable": {
			toml:     "nodes = 1\n",
			expected: []state.Problem{{Line: 1, Message: "nodes: must be a table of nodes keyed by ID"}},
		},
		"NodeNotATable": {
			toml:     "[nodes]\na = 1\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a: must be a table"}},
		},
		"NodeID": {
			toml:     "[nodes.Money_Utils]\n",
			expected: []state.Problem{{Line: 1, Message: "nodes.Money_Utils: node IDs must be kebab-case"}},
		},
		"UnknownNodeKey": {
			toml:     "[nodes.a]\ncolour = \"red\"\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a.colour: unknown key; a node holds depends_on, status, ingress, and egress"}},
		},
		"Status": {
			toml:     "[nodes.a]\nstatus = \"done\"\n",
			expected: []state.Problem{{Line: 2, Message: `nodes.a.status: must be "pending" or "migrated"`}},
		},
		"DependsOnNotIDs": {
			toml:     "[nodes.a]\ndepends_on = [1]\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a.depends_on: must be an array of node IDs"}},
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
		"UnsupportedKind": {
			toml:     "[nodes.a.ingress.kafka]\n",
			expected: []state.Problem{{Line: 1, Message: "nodes.a.ingress.kafka: ingress[kafka] is not a supported IO kind; run spectre state types for the supported ones"}},
		},
		"DirectionNotATable": {
			toml:     "[nodes.a]\negress = 1\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a.egress: must be a table keyed by protocol"}},
		},
		"KindNotATable": {
			toml:     "[nodes.a.egress]\nrpc = 1\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a.egress.rpc: must be a table of egress[rpc] settings"}},
		},
		"Settings": {
			toml: "[nodes.a.ingress.rpc]\ncolour = 1\ngroup = { name = \"x\" }\n",
			expected: []state.Problem{
				{Line: 1, Message: "nodes.a.ingress.rpc: methods is required"},
				{Line: 2, Message: "nodes.a.ingress.rpc.colour: unknown setting"},
				{Line: 3, Message: "nodes.a.ingress.rpc.group: must be a value, not a table"},
			},
		},
		"KeyedSettingNotATable": {
			toml:     "[nodes.a.ingress.rpc]\nmethods = [\"Get\"]\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a.ingress.rpc.methods: must be a table keyed by method name"}},
		},
		"Entries": {
			toml: "[nodes.a.ingress.rpc.methods]\n\"rpc.Get\" = 1\nPut = { retries = 2 }\n",
			expected: []state.Problem{
				{Line: 2, Message: `nodes.a.ingress.rpc.methods."rpc.Get": must be a table; write {} for an entry with no settings`},
				{Line: 3, Message: "nodes.a.ingress.rpc.methods.Put.retries: unknown setting"},
			},
		},
		"UniqueKeys": {
			toml: "[nodes.a.ingress.rpc.methods]\nGet = {}\n\n[nodes.b.ingress.rpc.methods]\nGet = {}\n",
			expected: []state.Problem{
				{Line: 5, Message: `nodes.b.ingress.rpc.methods.Get: also listed by node "a"`},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := state.Parse([]byte(test.toml), testCatalog(t))
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
[nodes.api.ingress.rpc.methods]

[nodes.client]
depends_on = ["money"]
[nodes.client.egress.rpc]

[nodes.money]

[nodes.job]
depends_on = ["money"]
`), testCatalog(t))
	assert.NoError(t, err)
	assert.Equal(t, []state.Problem{{Line: 12, Message: "nodes.job: no ingress node reaches it, so SPECTRE cannot verify it"}}, parsed.Unreached())
}

func TestNext(t *testing.T) {
	for name, test := range map[string]struct {
		toml     string
		expected []string
	}{
		"AllMigrated": {
			toml: "[nodes.api]\nstatus = \"migrated\"\n[nodes.api.ingress.rpc.methods]\n",
		},
		"FewestNodesWithIO": {
			toml: `
[nodes.money]

[nodes.ledger-client]
depends_on = ["money"]
[nodes.ledger-client.egress.rpc]

[nodes.invoices-api]
depends_on = ["money", "ledger-client"]
[nodes.invoices-api.ingress.rpc.methods]

[nodes.reports-api]
depends_on = ["money"]
[nodes.reports-api.ingress.rpc.methods]
`,
			expected: []string{"money", "reports-api"},
		},
		"FewerNodesWithIOBeforeFewerNodes": {
			toml: `
[nodes.a-api]
depends_on = ["client"]
[nodes.a-api.ingress.rpc.methods]

[nodes.client.egress.rpc]

[nodes.b-api]
depends_on = ["one", "two"]
[nodes.b-api.ingress.rpc.methods]

[nodes.one]
[nodes.two]
`,
			expected: []string{"one", "two", "b-api"},
		},
		"FewestNodes": {
			toml: `
[nodes.a-api]
depends_on = ["one", "two"]
[nodes.a-api.ingress.rpc.methods]

[nodes.b-api]
depends_on = ["one"]
[nodes.b-api.ingress.rpc.methods]

[nodes.one]
[nodes.two]
`,
			expected: []string{"one", "b-api"},
		},
		"LowestIDOnTie": {
			toml: `
[nodes.b-api.ingress.rpc.methods]
[nodes.a-api.ingress.rpc.methods]
`,
			expected: []string{"a-api"},
		},
		"IngressDependency": {
			toml: `
[nodes.api]
depends_on = ["auth"]
[nodes.api.ingress.rpc.methods]

[nodes.auth.ingress.rpc.methods]
`,
			expected: []string{"auth"},
		},
		"DependenciesFirst": {
			toml: `
[nodes.api]
depends_on = ["b", "a"]
[nodes.api.ingress.rpc.methods]

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
[nodes.api.ingress.rpc.methods]

[nodes.job]
`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			parsed, err := state.Parse([]byte(test.toml), testCatalog(t))
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
	parsed, err := state.Parse([]byte("[nodes.a]\n[nodes.b]\nstatus = \"migrated\"\n[nodes.c]\n"), testCatalog(t))
	assert.NoError(t, err)
	assert.Equal(t, []state.Node{node(t, parsed, "a"), node(t, parsed, "c")}, parsed.Pending())
}

func TestTypes(t *testing.T) {
	parsed, err := state.Parse([]byte("[nodes.api.egress.rpc]\n[nodes.api.ingress.rpc.methods]\n[nodes.money]\n"), testCatalog(t))
	assert.NoError(t, err)
	assert.Equal(t, []string{"ingress[rpc]", "egress[rpc]"}, node(t, parsed, "api").Types())
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
				"[nodes.b.ingress.rpc.methods]\nGet = {}\n\n[nodes]\na = {}\nc = { status = 'pending' }\n",
			ids: []string{"b", "a", "b"},
			expected: "[nodes]\n[nodes.a]\nstatus = 'migrated'\n\n[nodes.b]\ndepends_on = ['a']\nstatus = 'migrated'\n\n" +
				"[nodes.b.ingress]\n[nodes.b.ingress.rpc]\n[nodes.b.ingress.rpc.methods]\n[nodes.b.ingress.rpc.methods.Get]\n" +
				"[nodes.c]\nstatus = 'pending'\n",
			marked: []string{"b", "a"},
		},
		"AlreadyMigrated": {
			toml:     "# money\n[nodes.a]\nstatus = \"migrated\"\n",
			ids:      []string{"a"},
			expected: "# money\n[nodes.a]\nstatus = \"migrated\"\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			updated, marked, err := state.MarkMigrated([]byte(test.toml), testCatalog(t), test.ids)
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
			_, _, err := state.MarkMigrated([]byte(test.toml), testCatalog(t), test.ids)
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

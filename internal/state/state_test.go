package state_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/state"
)

var (
	rpcIngress = boundary.Kind{Direction: boundary.Ingress, Protocol: "rpc"}
	rpcEgress  = boundary.Kind{Direction: boundary.Egress, Protocol: "rpc"}
)

func testCatalog(t *testing.T) *boundary.Catalog {
	t.Helper()
	catalog, err := boundary.NewCatalog(
		boundary.Support{Kind: rpcEgress},
		boundary.Support{Kind: rpcIngress, Settings: []boundary.Setting{
			{Name: "methods", Required: true, Key: "method name", Unique: true, Entry: []boundary.Setting{{Name: "timeout"}}},
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
				{ID: "api", Line: 5, DependsOn: []string{"money"}, Status: state.Pending, IO: map[boundary.Kind]map[string]any{
					rpcIngress: {"group": "public", "methods": map[string]any{"Get": map[string]any{}, "Put": map[string]any{"timeout": "1s"}}},
					rpcEgress:  {},
				}},
				{ID: "money", Line: 2, Status: state.Migrated, IO: map[boundary.Kind]map[string]any{}},
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
			expected: []state.Problem{{Line: 3, Message: "expecting a value"}},
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
				{Line: 1, Message: "nodes.a.ingress.rpc.group: must be a value, not a table"},
				{Line: 2, Message: "nodes.a.ingress.rpc.colour: unknown setting"},
			},
		},
		"KeyedSettingNotATable": {
			toml:     "[nodes.a.ingress.rpc]\nmethods = [\"Get\"]\n",
			expected: []state.Problem{{Line: 2, Message: "nodes.a.ingress.rpc.methods: must be a table keyed by method name"}},
		},
		"Entries": {
			toml: "[nodes.a.ingress.rpc.methods]\n\"rpc.Get\" = 1\nPut = { retries = 2 }\n",
			expected: []state.Problem{
				{Line: 1, Message: "nodes.a.ingress.rpc.methods.Put.retries: unknown setting"},
				{Line: 2, Message: `nodes.a.ingress.rpc.methods."rpc.Get": must be a table; write {} for an entry with no settings`},
			},
		},
		"UniqueKeys": {
			toml: "[nodes.a.ingress.rpc.methods]\nGet = {}\n\n[nodes.b.ingress.rpc.methods]\nGet = {}\n",
			expected: []state.Problem{
				{Line: 4, Message: `nodes.b.ingress.rpc.methods.Get: also listed by node "a"`},
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

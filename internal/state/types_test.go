package state_test

import (
	"bytes"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/state"
)

func TestWriteTypes(t *testing.T) {
	catalog, err := boundary.NewCatalog(
		boundary.Support{Kind: boundary.Kind{Direction: boundary.Egress, Protocol: "rpc"}, Description: "Calls <dependencies>."},
		boundary.Support{Kind: boundary.Kind{Direction: boundary.Ingress, Protocol: "rpc"}, Description: "Serves requests."},
	)
	assert.NoError(t, err)
	library := state.NodeTypes(catalog)[0].Description

	for name, test := range map[string]struct {
		config   state.TypesConfig
		expected string
	}{
		"Markdown": {
			expected: "## library\n\n" + library + "\n\n" +
				"## ingress[rpc]\n\nServes requests.\n\n" +
				"## egress[rpc]\n\nCalls <dependencies>.\n",
		},
		"JSON": {
			config: state.TypesConfig{JSON: true},
			expected: `[
  {
    "name": "library",
    "description": "` + library + `"
  },
  {
    "name": "ingress[rpc]",
    "description": "Serves requests."
  },
  {
    "name": "egress[rpc]",
    "description": "Calls <dependencies>."
  }
]
`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer

			assert.NoError(t, state.WriteTypes(&out, test.config, catalog))

			assert.Equal(t, test.expected, out.String())
		})
	}
}

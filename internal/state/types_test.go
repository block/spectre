package state_test

import (
	"bytes"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/state"
	"github.com/block/spectre/internal/terminal"
)

func TestWriteTypes(t *testing.T) {
	catalog, err := boundary.NewCatalog(
		boundary.Support{Kind: boundary.Kind{Direction: boundary.Egress, Protocol: "rpc"}, Description: "Calls <dependencies>."},
		boundary.Support{
			Kind:        boundary.Kind{Direction: boundary.Ingress, Protocol: "rpc"},
			Description: "Serves requests.",
			Settings: []boundary.Setting{
				{Name: "methods", Required: true, Description: "Each method served."},
				{Name: "group", Description: "The server group."},
			},
		},
	)
	assert.NoError(t, err)
	library := state.NodeTypes(catalog)[0].Description

	for name, test := range map[string]struct {
		config   state.TypesConfig
		palette  terminal.Palette
		expected string
	}{
		"Text": {
			palette: terminal.NewPalette(false),
			expected: "library\n" + terminal.Wrap(library, "  ", 80) + "\n" +
				"ingress[rpc] [nodes.<id>.ingress.rpc]\n  Serves requests.\n\n" +
				"  methods (required)\n    Each method served.\n\n" +
				"  group (optional)\n    The server group.\n\n" +
				"egress[rpc] [nodes.<id>.egress.rpc]\n  Calls <dependencies>.\n\n" +
				"  No settings.\n",
		},
		"Color": {
			palette: terminal.NewPalette(true),
			expected: "\033[1mlibrary\033[0m\n" + terminal.Wrap(library, "  ", 80) + "\n" +
				"\033[1mingress[rpc]\033[0m \033[2m[nodes.<id>.ingress.rpc]\033[0m\n  Serves requests.\n\n" +
				"  \033[32mmethods\033[0m \033[33m(required)\033[0m\n    Each method served.\n\n" +
				"  \033[32mgroup\033[0m \033[2m(optional)\033[0m\n    The server group.\n\n" +
				"\033[1megress[rpc]\033[0m \033[2m[nodes.<id>.egress.rpc]\033[0m\n  Calls <dependencies>.\n\n" +
				"  \033[2mNo settings.\033[0m\n",
		},
		"JSON": {
			config:  state.TypesConfig{JSON: true},
			palette: terminal.NewPalette(true),
			expected: `[
  {
    "name": "library",
    "description": "` + library + `"
  },
  {
    "name": "ingress[rpc]",
    "description": "Serves requests.",
    "table": "nodes.<id>.ingress.rpc",
    "settings": [
      {
        "name": "methods",
        "required": true,
        "description": "Each method served."
      },
      {
        "name": "group",
        "required": false,
        "description": "The server group."
      }
    ]
  },
  {
    "name": "egress[rpc]",
    "description": "Calls <dependencies>.",
    "table": "nodes.<id>.egress.rpc"
  }
]
`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer

			assert.NoError(t, state.WriteTypes(&out, test.config, test.palette, catalog))

			assert.Equal(t, test.expected, out.String())
		})
	}
}

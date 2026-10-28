package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/cli/terminal"
	"github.com/block/spectre/internal/state"
)

func TestStateTypesCommand(t *testing.T) {
	catalog, err := boundary.NewCatalog(
		boundary.Support{Kind: boundary.Kind{Direction: boundary.Egress, Protocol: "rpc"}, Description: "Calls <dependencies>."},
		boundary.Support{
			Kind:        boundary.Kind{Direction: boundary.Ingress, Protocol: "rpc"},
			Description: "Serves requests.",
			Settings: []boundary.Setting{
				{Name: "methods", Required: true, Description: "Each method served.", Key: "method name", Entry: []boundary.Setting{
					{Name: "timeout", Description: "How long a call may take."},
				}},
				{Name: "services", Description: "Each service served.", Key: "service name"},
				{Name: "group", Description: "The server group."},
			},
		},
	)
	assert.NoError(t, err)
	library := state.NodeTypes(catalog)[0].Description

	for name, test := range map[string]struct {
		command  stateTypesCommand
		palette  terminal.Palette
		expected string
	}{
		"Text": {
			palette: terminal.NewPalette(false),
			expected: "library\n" + terminal.Wrap(library, "  ", 80) + "\n" +
				"ingress[rpc] [nodes.<id>.ingress.rpc]\n  Serves requests.\n\n" +
				"  methods (required)\n    Each method served. A table keyed by method name.\n" +
				"    Each entry is a table with these settings:\n\n" +
				"      timeout (optional)\n        How long a call may take.\n\n" +
				"  services (optional)\n    Each service served. A table keyed by service name.\n" +
				"    Each entry takes no settings yet, so write it as {}.\n\n" +
				"  group (optional)\n    The server group.\n\n" +
				"egress[rpc] [nodes.<id>.egress.rpc]\n  Calls <dependencies>.\n\n" +
				"  No settings.\n",
		},
		"Color": {
			palette: terminal.NewPalette(true),
			expected: "\033[1mlibrary\033[0m\n" + terminal.Wrap(library, "  ", 80) + "\n" +
				"\033[1mingress[rpc]\033[0m \033[2m[nodes.<id>.ingress.rpc]\033[0m\n  Serves requests.\n\n" +
				"  \033[32mmethods\033[0m \033[33m(required)\033[0m\n    Each method served. A table keyed by method name.\n" +
				"    Each entry is a table with these settings:\n\n" +
				"      \033[32mtimeout\033[0m \033[2m(optional)\033[0m\n        How long a call may take.\n\n" +
				"  \033[32mservices\033[0m \033[2m(optional)\033[0m\n    Each service served. A table keyed by service name.\n" +
				"    Each entry takes no settings yet, so write it as {}.\n\n" +
				"  \033[32mgroup\033[0m \033[2m(optional)\033[0m\n    The server group.\n\n" +
				"\033[1megress[rpc]\033[0m \033[2m[nodes.<id>.egress.rpc]\033[0m\n  Calls <dependencies>.\n\n" +
				"  \033[2mNo settings.\033[0m\n",
		},
		"JSON": {
			command: stateTypesCommand{JSON: true},
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
        "description": "Each method served.",
        "key": "method name",
        "entry": [
          {
            "name": "timeout",
            "required": false,
            "description": "How long a call may take."
          }
        ]
      },
      {
        "name": "services",
        "required": false,
        "description": "Each service served.",
        "key": "service name"
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

			assert.NoError(t, test.command.Run(Output{Writer: &out, Palette: test.palette}, catalog))

			assert.Equal(t, test.expected, out.String())
		})
	}
}

func testCatalog(t *testing.T) *boundary.Catalog {
	t.Helper()
	catalog, err := boundary.NewCatalog(boundary.Support{
		Kind:     boundary.Kind{Direction: boundary.Ingress, Protocol: "rpc"},
		Settings: []boundary.Setting{{Name: "methods", Required: true, Key: "method name"}},
	})
	assert.NoError(t, err)
	return catalog
}

func TestStateValidateCommand(t *testing.T) {
	for name, test := range map[string]struct {
		toml     string
		expected string
		err      string
	}{
		"Valid": {
			toml:     "[nodes.api.ingress.rpc.methods]\n\n[nodes.job]\nstatus = \"migrated\"\n",
			expected: "STATE:3: warning: nodes.job: no ingress node reaches it, so SPECTRE cannot verify it\nSTATE is valid: 2 nodes, 1 migrated\n",
		},
		"Invalid": {
			toml:     "[nodes.a]\nstatus = \"done\"\ncolour = \"red\"\n",
			expected: "STATE:2: error: nodes.a.status: must be \"pending\" or \"migrated\"\nSTATE:3: error: nodes.a.colour: unknown key; a node holds depends_on, status, ingress, and egress\n",
			err:      "STATE is invalid",
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.toml")
			assert.NoError(t, os.WriteFile(path, []byte(test.toml), 0o600))
			command := stateValidateCommand{State: state.Config{StateFile: path}}
			var out bytes.Buffer

			err := command.Run(Output{Writer: &out}, testCatalog(t))

			if test.err != "" {
				assert.EqualError(t, err, replacePath(test.err, path))
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, replacePath(test.expected, path), out.String())
		})
	}
}

func TestStateValidateCommandMissingFile(t *testing.T) {
	command := stateValidateCommand{State: state.Config{StateFile: filepath.Join(t.TempDir(), "state.toml")}}
	err := command.Run(Output{Writer: &bytes.Buffer{}}, testCatalog(t))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read migration state")
}

func replacePath(text, path string) string {
	return string(bytes.ReplaceAll([]byte(text), []byte("STATE"), []byte(path)))
}

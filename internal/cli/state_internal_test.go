package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/cli/terminal"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/state"
)

func TestStateTypesCommand(t *testing.T) {
	catalog, err := state.NewCatalog(
		state.Support{Kind: model.IOKind{Direction: model.Egress, Protocol: "rpc"}, Description: "Calls <dependencies>."},
		state.Support{
			Kind:        model.IOKind{Direction: model.Ingress, Protocol: "rpc"},
			Description: "Serves requests.",
			Settings: []state.Setting{
				{Name: "methods", Required: true, Description: "Each method served.", Key: "method name", Entry: []state.Setting{
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

func testCatalog(t *testing.T) *state.Catalog {
	t.Helper()
	catalog, err := state.NewCatalog(state.Support{
		Kind:     model.IOKind{Direction: model.Ingress, Protocol: "rpc"},
		Settings: []state.Setting{{Name: "methods", Required: true, Key: "method name"}},
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

func TestNextCommand(t *testing.T) {
	const twoSteps = "[nodes.api]\ndepends_on = [\"money\"]\n[nodes.api.ingress.rpc.methods]\n\n[nodes.money]\n\n" +
		"[nodes.other.ingress.rpc.methods]\nGet = {}\n[nodes.other]\ndepends_on = [\"api\"]\n"
	for name, test := range map[string]struct {
		toml     string
		command  nextCommand
		palette  terminal.Palette
		expected string
		err      string
	}{
		"Text": {
			toml:     twoSteps,
			expected: "money library\napi ingress[rpc]\n",
		},
		"Color": {
			toml:     twoSteps,
			palette:  terminal.NewPalette(true),
			expected: "\033[1mmoney\033[0m \033[2mlibrary\033[0m\n\033[1mapi\033[0m \033[2mingress[rpc]\033[0m\n",
		},
		"JSON": {
			toml:    twoSteps,
			command: nextCommand{JSON: true},
			expected: `[
  {
    "id": "money",
    "status": "pending",
    "io": {}
  },
  {
    "id": "api",
    "depends_on": [
      "money"
    ],
    "status": "pending",
    "io": {
      "ingress[rpc]": {
        "methods": {}
      }
    }
  }
]
`,
		},
		"AllMigrated": {
			toml:     "[nodes.api]\nstatus = \"migrated\"\n[nodes.api.ingress.rpc.methods]\n",
			expected: "Every node is migrated.\n",
		},
		"AllMigratedJSON": {
			toml:     "[nodes.api]\nstatus = \"migrated\"\n[nodes.api.ingress.rpc.methods]\n",
			command:  nextCommand{JSON: true},
			expected: "[]\n",
		},
		"Unreached": {
			toml: "[nodes.job]\n[nodes.cron]\n",
			err:  "no ingress node reaches the pending nodes, so SPECTRE cannot verify them: cron, job",
		},
		"Invalid": {
			toml:     "[nodes.a]\nstatus = \"done\"\n",
			expected: "STATE:2: error: nodes.a.status: must be \"pending\" or \"migrated\"\n",
			err:      "STATE is invalid",
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.toml")
			assert.NoError(t, os.WriteFile(path, []byte(test.toml), 0o600))
			test.command.State = state.Config{StateFile: path}
			var out bytes.Buffer

			err := test.command.Run(Output{Writer: &out, Palette: test.palette}, testCatalog(t))

			if test.err != "" {
				assert.EqualError(t, err, replacePath(test.err, path))
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, replacePath(test.expected, path), out.String())
		})
	}
}

func TestDoneCommand(t *testing.T) {
	for name, test := range map[string]struct {
		toml     string
		nodes    []string
		expected string
		file     string
		err      string
	}{
		"Marks": {
			toml:     "[nodes.api]\ndepends_on = [\"money\"]\n[nodes.api.ingress.rpc.methods]\n\n[nodes.money]\nstatus = \"pending\"\n",
			nodes:    []string{"money", "api"},
			expected: "STATE: marked money, api migrated\n",
			file: "[nodes]\n[nodes.api]\ndepends_on = ['money']\nstatus = 'migrated'\n\n" +
				"[nodes.api.ingress]\n[nodes.api.ingress.rpc]\n[nodes.api.ingress.rpc.methods]\n[nodes.money]\nstatus = 'migrated'\n",
		},
		"AlreadyMigrated": {
			toml:     "[nodes.money]\nstatus = \"migrated\"\n",
			nodes:    []string{"money"},
			expected: "STATE is unchanged: every node is already migrated\n",
			file:     "[nodes.money]\nstatus = \"migrated\"\n",
		},
		"WouldBeInvalid": {
			toml:     "[nodes.api]\ndepends_on = [\"money\"]\n\n[nodes.money]\n",
			nodes:    []string{"api"},
			expected: "STATE: error: nodes.api.status: migrated, but depends on pending node \"money\"\n",
			file:     "[nodes.api]\ndepends_on = [\"money\"]\n\n[nodes.money]\n",
			err:      "STATE would be invalid, so it is unchanged",
		},
		"UnknownNode": {
			toml:  "[nodes.money]\n",
			nodes: []string{"api"},
			file:  "[nodes.money]\n",
			err:   "not a node: api",
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.toml")
			assert.NoError(t, os.WriteFile(path, []byte(test.toml), 0o600))
			command := doneCommand{State: state.Config{StateFile: path}, Nodes: test.nodes}
			var out bytes.Buffer

			err := command.Run(Output{Writer: &out}, testCatalog(t))

			if test.err != "" {
				assert.EqualError(t, err, replacePath(test.err, path))
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, replacePath(test.expected, path), out.String())
			file, err := os.ReadFile(path)
			assert.NoError(t, err)
			assert.Equal(t, test.file, string(file))
		})
	}
}

func TestDoneCommandMissingFile(t *testing.T) {
	command := doneCommand{State: state.Config{StateFile: filepath.Join(t.TempDir(), "state.toml")}, Nodes: []string{"a"}}
	err := command.Run(Output{Writer: &bytes.Buffer{}}, testCatalog(t))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read migration state")
}

package spectre

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/supervisor"
)

func TestRunCommandEmbedsConfig(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	command := &cli{}
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{
		"run",
		"--candidate=run-candidate",
		"--reference=run-reference",
		"--destination=forecasts.example=http://127.0.0.1:50053",
		"--match-window=2s",
		"--scripts-dir=" + scripts,
		"--schema-dir=" + schema,
		"--descriptors-dir=" + descriptors,
		"--no-limits",
	})
	assert.NoError(t, err)
	expected := supervisor.NewConfig()
	expected.Candidate = "run-candidate"
	expected.Reference = "run-reference"
	expected.Destinations = map[string]string{"forecasts.example": "http://127.0.0.1:50053"}
	expected.MatchWindow = 2 * time.Second
	expected.ScriptsDir = scripts
	expected.SchemaDir = schema
	expected.Descriptors.DescriptorsDir = descriptors
	expected.Limits.Enabled = false
	assert.Equal(t, expected, command.Run.Supervisor)
}

func TestRunCommandRequiresCandidateAndReference(t *testing.T) {
	parser, err := newParser(&cli{})
	assert.NoError(t, err)

	_, err = parser.Parse([]string{"run", "--scripts-dir=" + t.TempDir(), "--schema-dir=" + writeSchemaDir(t)})

	assert.Error(t, err)
}

func TestRunCommandLoadsConfigFile(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	path := filepath.Join(t.TempDir(), "spectre.toml")
	err := os.WriteFile(path, []byte(fmt.Sprintf(`
candidate = "run-candidate"
reference = "run-reference"
match-window = "2s"
scripts-dir = %q
schema-dir = %q
descriptors-dir = %q

[destination]
"forecasts.example" = "http://127.0.0.1:50053"
`, scripts, schema, descriptors)), 0o600)
	assert.NoError(t, err)
	command := &cli{}
	parser, err := newParser(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{"run", "--config=" + path, "--match-window=3s"})
	assert.NoError(t, err)
	expected := supervisor.NewConfig()
	expected.Candidate = "run-candidate"
	expected.Reference = "run-reference"
	expected.Destinations = map[string]string{"forecasts.example": "http://127.0.0.1:50053"}
	expected.MatchWindow = 3 * time.Second
	expected.ScriptsDir = scripts
	expected.SchemaDir = schema
	expected.Descriptors.DescriptorsDir = descriptors
	assert.Equal(t, expected, command.Run.Supervisor)
}

func TestRunCommandRejectsUnknownConfigKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spectre.toml")
	assert.NoError(t, os.WriteFile(path, []byte(`canddate = "run-candidate"`), 0o600))
	parser, err := newParser(&cli{})
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		"run",
		"--config=" + path,
		"--candidate=run-candidate",
		"--reference=run-reference",
		"--scripts-dir=" + t.TempDir(),
		"--schema-dir=" + writeSchemaDir(t),
	})

	assert.EqualError(t, err, path+": unknown configuration keys: canddate")
}

func TestProxySubcommandsRemoved(t *testing.T) {
	for _, name := range []string{"__ingress", "__egress"} {
		parser, err := newParser(&cli{})
		assert.NoError(t, err)
		_, err = parser.Parse([]string{name})
		assert.Error(t, err)
	}
}

func writeSchemaDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "payload.d.ts"), []byte(`declare module "payload" { interface Payload { value: string; } }`), 0o600))
	return dir
}

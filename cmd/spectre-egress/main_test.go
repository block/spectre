package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/kong"
	kongtoml "github.com/alecthomas/kong-toml"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/egress"
)

func TestCLIEmbedsEgressConfig(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{
		"--candidate-listen=unix:/tmp/candidate.sock",
		"--destination=api.weather.example=https://api.weather.example",
		"--destination=users.example=h2c://127.0.0.1:9000",
		"--match-window=2s",
		"--scripts-dir=" + scripts,
		"--schema-dir=" + schema,
		"--descriptors-dir=" + descriptors,
	})
	assert.NoError(t, err)
	expected := egress.NewConfig()
	expected.CandidateListen = "unix:/tmp/candidate.sock"
	expected.Destinations = map[string]string{
		"api.weather.example": "https://api.weather.example",
		"users.example":       "h2c://127.0.0.1:9000",
	}
	expected.MatchWindow = 2 * time.Second
	expected.Descriptors.DescriptorsDir = descriptors
	expectedComparison := comparison.NewConfig()
	expectedComparison.ScriptsDir = scripts
	expectedComparison.Schema.SchemaDir = schema
	assert.Equal(t, expected, command.Egress)
	assert.Equal(t, expectedComparison, command.Comparison)
}

func TestCLIRequiresSchemaDir(t *testing.T) {
	parser, err := kong.New(&cli{})
	assert.NoError(t, err)

	_, err = parser.Parse([]string{"--scripts-dir=" + t.TempDir()})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schema directory is required")
}

func TestCLIAllowsOmittingDescriptors(t *testing.T) {
	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		"--scripts-dir=" + t.TempDir(),
		"--schema-dir=" + writeSchemaDir(t),
	})

	assert.NoError(t, err)
	assert.NoError(t, command.Egress.Validate())
}

func TestCLILoadsConfigFile(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	path := filepath.Join(t.TempDir(), "spectre-egress.toml")
	err := os.WriteFile(path, []byte(fmt.Sprintf(`
candidate-listen = "unix:/tmp/candidate.sock"
match-window = "2s"
scripts-dir = %q
schema-dir = %q
descriptors-dir = %q

[destination]
"api.weather.example" = "https://api.weather.example"
"users.example" = "h2c://127.0.0.1:9000"
`, scripts, schema, descriptors)), 0o600)
	assert.NoError(t, err)
	command := &cli{}
	parser, err := kong.New(command, kong.Configuration(kongtoml.Loader))
	assert.NoError(t, err)
	_, err = parser.Parse([]string{"--config=" + path, "--match-window=3s"})
	assert.NoError(t, err)
	expected := egress.NewConfig()
	expected.CandidateListen = "unix:/tmp/candidate.sock"
	expected.Destinations = map[string]string{
		"api.weather.example": "https://api.weather.example",
		"users.example":       "h2c://127.0.0.1:9000",
	}
	expected.MatchWindow = 3 * time.Second
	expected.Descriptors.DescriptorsDir = descriptors
	expectedComparison := comparison.NewConfig()
	expectedComparison.ScriptsDir = scripts
	expectedComparison.Schema.SchemaDir = schema
	assert.Equal(t, expected, command.Egress)
	assert.Equal(t, expectedComparison, command.Comparison)
}

func writeSchemaDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "payload.d.ts"), []byte(`declare module "payload" { interface Payload { value: string; } }`), 0o600))
	return dir
}

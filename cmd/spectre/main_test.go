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

	"github.com/block/spectre/internal/httpserver"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/supervisor"
)

func TestIngressCommandEmbedsConfig(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{
		supervisor.IngressCommand,
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
		"--scripts-dir=" + scripts,
		"--no-reflection",
		"--schema-dir=" + schema,
		"--descriptors-dir=" + descriptors,
	})
	assert.NoError(t, err)
	expected := httpserver.NewIngressConfig()
	expected.Reference = "http://reference.example"
	expected.Candidate = "http://candidate.example"
	expected.Reflection = false
	expected.Descriptors.DescriptorsDir = descriptors
	expectedEngine := protocol.NewConfig()
	expectedEngine.Comparison.ScriptsDir = scripts
	expectedEngine.Comparison.Schema.SchemaDir = schema
	assert.Equal(t, expected, command.Ingress.Ingress)
	assert.Equal(t, expectedEngine, command.Ingress.Engine)
}

func TestIngressCommandRequiresScriptsDir(t *testing.T) {
	parser, err := kong.New(&cli{})
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		supervisor.IngressCommand,
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
		"--schema-dir=" + writeSchemaDir(t),
	})

	assert.Error(t, err)
}

func TestIngressCommandRequiresSchemaDir(t *testing.T) {
	parser, err := kong.New(&cli{})
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		supervisor.IngressCommand,
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
		"--scripts-dir=" + t.TempDir(),
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schema directory is required")
}

func TestIngressCommandAllowsOmittingDescriptors(t *testing.T) {
	for _, test := range []struct {
		name       string
		reflection bool
	}{
		{name: "Reflection", reflection: true},
		{name: "NoReflection"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := &cli{}
			parser, err := kong.New(command)
			assert.NoError(t, err)
			arguments := []string{
				supervisor.IngressCommand,
				"--reference=http://reference.example",
				"--candidate=http://candidate.example",
				"--scripts-dir=" + t.TempDir(),
				"--schema-dir=" + writeSchemaDir(t),
			}
			if !test.reflection {
				arguments = append(arguments, "--no-reflection")
			}
			_, err = parser.Parse(arguments)
			assert.NoError(t, err)
			assert.NoError(t, command.Ingress.Ingress.Validate())
		})
	}
}

func TestIngressCommandLoadsConfigFile(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	path := filepath.Join(t.TempDir(), "ingress.toml")
	err := os.WriteFile(path, []byte(fmt.Sprintf(`
reference = "http://reference.example"
candidate = "http://candidate.example"
reflection = false
scripts-dir = %q
schema-dir = %q
descriptors-dir = %q
`, scripts, schema, descriptors)), 0o600)
	assert.NoError(t, err)
	command := &cli{}
	parser, err := kong.New(command, kong.Configuration(kongtoml.Loader))
	assert.NoError(t, err)
	_, err = parser.Parse([]string{supervisor.IngressCommand, "--config=" + path, "--candidate=http://override.example"})
	assert.NoError(t, err)
	expected := httpserver.NewIngressConfig()
	expected.Reference = "http://reference.example"
	expected.Candidate = "http://override.example"
	expected.Reflection = false
	expected.Descriptors.DescriptorsDir = descriptors
	expectedEngine := protocol.NewConfig()
	expectedEngine.Comparison.ScriptsDir = scripts
	expectedEngine.Comparison.Schema.SchemaDir = schema
	assert.Equal(t, expected, command.Ingress.Ingress)
	assert.Equal(t, expectedEngine, command.Ingress.Engine)
}

func TestIngressCommandRejectsUnknownConfigKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingress.toml")
	assert.NoError(t, os.WriteFile(path, []byte(`refrence = "http://reference.example"`), 0o600))
	parser, err := kong.New(&cli{}, kong.Configuration(kongtoml.Loader))
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		supervisor.IngressCommand,
		"--config=" + path,
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
		"--scripts-dir=" + t.TempDir(),
		"--schema-dir=" + writeSchemaDir(t),
	})

	assert.EqualError(t, err, path+": unknown configuration keys: refrence")
}

func TestEgressCommandEmbedsConfig(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{
		supervisor.EgressCommand,
		"--candidate-listen=unix:/tmp/candidate.sock",
		"--destination=api.weather.example=https://api.weather.example",
		"--destination=users.example=h2c://127.0.0.1:9000",
		"--match-window=2s",
		"--scripts-dir=" + scripts,
		"--schema-dir=" + schema,
		"--descriptors-dir=" + descriptors,
	})
	assert.NoError(t, err)
	expected := httpserver.NewEgressConfig()
	expected.CandidateListen = "unix:/tmp/candidate.sock"
	expected.Destinations = map[string]string{
		"api.weather.example": "https://api.weather.example",
		"users.example":       "h2c://127.0.0.1:9000",
	}
	expected.Descriptors.DescriptorsDir = descriptors
	expectedEngine := protocol.NewConfig()
	expectedEngine.Comparison.ScriptsDir = scripts
	expectedEngine.Comparison.Schema.SchemaDir = schema
	expectedEngine.MatchWindow = 2 * time.Second
	assert.Equal(t, expected, command.Egress.Egress)
	assert.Equal(t, expectedEngine, command.Egress.Engine)
}

func TestEgressCommandRequiresSchemaDir(t *testing.T) {
	parser, err := kong.New(&cli{})
	assert.NoError(t, err)

	_, err = parser.Parse([]string{supervisor.EgressCommand, "--scripts-dir=" + t.TempDir()})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schema directory is required")
}

func TestEgressCommandAllowsOmittingDescriptors(t *testing.T) {
	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		supervisor.EgressCommand,
		"--scripts-dir=" + t.TempDir(),
		"--schema-dir=" + writeSchemaDir(t),
	})

	assert.NoError(t, err)
	assert.NoError(t, command.Egress.Egress.Validate())
}

func TestEgressCommandLoadsConfigFile(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	path := filepath.Join(t.TempDir(), "egress.toml")
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
	_, err = parser.Parse([]string{supervisor.EgressCommand, "--config=" + path, "--match-window=3s"})
	assert.NoError(t, err)
	expected := httpserver.NewEgressConfig()
	expected.CandidateListen = "unix:/tmp/candidate.sock"
	expected.Destinations = map[string]string{
		"api.weather.example": "https://api.weather.example",
		"users.example":       "h2c://127.0.0.1:9000",
	}
	expected.Descriptors.DescriptorsDir = descriptors
	expectedEngine := protocol.NewConfig()
	expectedEngine.Comparison.ScriptsDir = scripts
	expectedEngine.Comparison.Schema.SchemaDir = schema
	expectedEngine.MatchWindow = 3 * time.Second
	assert.Equal(t, expected, command.Egress.Egress)
	assert.Equal(t, expectedEngine, command.Egress.Engine)
}

func writeSchemaDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "payload.d.ts"), []byte(`declare module "payload" { interface Payload { value: string; } }`), 0o600))
	return dir
}

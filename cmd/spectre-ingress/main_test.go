package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/kong"
	kongtoml "github.com/alecthomas/kong-toml"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/ingress"
)

func TestCLIEmbedsIngressConfig(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
		"--scripts-dir=" + scripts,
		"--no-reflection",
		"--schema-dir=" + schema,
		"--descriptors-dir=" + descriptors,
	})
	assert.NoError(t, err)
	expected := ingress.NewConfig()
	expected.Reference = "http://reference.example"
	expected.Candidate = "http://candidate.example"
	expected.Reflection = false
	expected.Descriptors.DescriptorsDir = descriptors
	expectedComparison := comparison.NewConfig()
	expectedComparison.ScriptsDir = scripts
	expectedComparison.Schema.SchemaDir = schema
	assert.Equal(t, expected, command.Ingress)
	assert.Equal(t, expectedComparison, command.Comparison)
}

func TestCLIRequiresScriptsDir(t *testing.T) {
	parser, err := kong.New(&cli{})
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
		"--schema-dir=" + writeSchemaDir(t),
	})

	assert.Error(t, err)
}

func TestCLIRequiresSchemaDir(t *testing.T) {
	parser, err := kong.New(&cli{})
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
		"--scripts-dir=" + t.TempDir(),
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schema directory is required")
}

func TestCLIAllowsOmittingDescriptors(t *testing.T) {
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
			assert.NoError(t, command.Ingress.Validate())
		})
	}
}

func TestCLILoadsConfigFile(t *testing.T) {
	scripts := t.TempDir()
	schema := writeSchemaDir(t)
	descriptors := t.TempDir()
	path := filepath.Join(t.TempDir(), "spectre-ingress.toml")
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
	_, err = parser.Parse([]string{"--config=" + path, "--candidate=http://override.example"})
	assert.NoError(t, err)
	expected := ingress.NewConfig()
	expected.Reference = "http://reference.example"
	expected.Candidate = "http://override.example"
	expected.Reflection = false
	expected.Descriptors.DescriptorsDir = descriptors
	expectedComparison := comparison.NewConfig()
	expectedComparison.ScriptsDir = scripts
	expectedComparison.Schema.SchemaDir = schema
	assert.Equal(t, expected, command.Ingress)
	assert.Equal(t, expectedComparison, command.Comparison)
}

func TestCLIRejectsUnknownConfigKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spectre-ingress.toml")
	assert.NoError(t, os.WriteFile(path, []byte(`refrence = "http://reference.example"`), 0o600))
	parser, err := kong.New(&cli{}, kong.Configuration(kongtoml.Loader))
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		"--config=" + path,
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
		"--scripts-dir=" + t.TempDir(),
		"--schema-dir=" + writeSchemaDir(t),
	})

	assert.EqualError(t, err, path+": unknown configuration keys: refrence")
}

func writeSchemaDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "payload.d.ts"), []byte(`declare module "payload" { interface Payload { value: string; } }`), 0o600))
	return dir
}

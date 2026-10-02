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
	descriptors := t.TempDir()
	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
		"--scripts-dir=" + scripts,
		"--no-reflection",
		"--descriptors-dir=" + descriptors,
	})
	assert.NoError(t, err)
	expected := ingress.NewConfig()
	expected.Reference = "http://reference.example"
	expected.Candidate = "http://candidate.example"
	expected.Reflection = false
	expected.Schema.DescriptorsDir = descriptors
	expectedComparison := comparison.NewConfig()
	expectedComparison.ScriptsDir = scripts
	assert.Equal(t, expected, command.Ingress)
	assert.Equal(t, expectedComparison, command.Comparison)
}

func TestCLIRequiresScriptsDir(t *testing.T) {
	parser, err := kong.New(&cli{})
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
	})

	assert.Error(t, err)
}

func TestCLILoadsConfigFile(t *testing.T) {
	scripts := t.TempDir()
	descriptors := t.TempDir()
	path := filepath.Join(t.TempDir(), "spectre-ingress.toml")
	err := os.WriteFile(path, []byte(fmt.Sprintf(`
reference = "http://reference.example"
candidate = "http://candidate.example"
reflection = false
scripts-dir = %q
descriptors-dir = %q
`, scripts, descriptors)), 0o600)
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
	expected.Schema.DescriptorsDir = descriptors
	expectedComparison := comparison.NewConfig()
	expectedComparison.ScriptsDir = scripts
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
	})

	assert.EqualError(t, err, path+": unknown configuration keys: refrence")
}

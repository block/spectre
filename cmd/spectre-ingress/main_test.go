package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/ingress"
)

func TestCLIEmbedsIngressConfig(t *testing.T) {
	scripts := t.TempDir()
	descriptors := filepath.Join(t.TempDir(), "descriptors.binpb")
	assert.NoError(t, os.WriteFile(descriptors, nil, 0o600))
	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
		"--scripts-dir=" + scripts,
		"--no-reflection",
		"--descriptor-set=" + descriptors,
	})
	assert.NoError(t, err)
	expected := ingress.NewConfig()
	expected.Reference = "http://reference.example"
	expected.Candidate = "http://candidate.example"
	expected.Reflection = false
	expected.Schema.DescriptorSets = []string{descriptors}
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

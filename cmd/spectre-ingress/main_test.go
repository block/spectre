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
	script := filepath.Join(t.TempDir(), "comparison.js")
	assert.NoError(t, os.WriteFile(script, nil, 0o600))
	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
		"--comparison-script=" + script,
	})
	assert.NoError(t, err)
	expected := ingress.NewConfig()
	expected.Reference = "http://reference.example"
	expected.Candidate = "http://candidate.example"
	expectedComparison := comparison.NewConfig()
	expectedComparison.ComparisonScript = script
	assert.Equal(t, expected, command.Ingress)
	assert.Equal(t, expectedComparison, command.Comparison)
}

func TestCLIRequiresComparisonScript(t *testing.T) {
	parser, err := kong.New(&cli{})
	assert.NoError(t, err)

	_, err = parser.Parse([]string{
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
	})

	assert.Error(t, err)
}

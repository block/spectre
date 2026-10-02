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
	expected.Schema.DescriptorsDir = descriptors
	expectedComparison := comparison.NewConfig()
	expectedComparison.ScriptsDir = scripts
	assert.Equal(t, expected, command.Egress)
	assert.Equal(t, expectedComparison, command.Comparison)
}

func TestCLILoadsConfigFile(t *testing.T) {
	scripts := t.TempDir()
	descriptors := t.TempDir()
	path := filepath.Join(t.TempDir(), "spectre-egress.toml")
	err := os.WriteFile(path, []byte(fmt.Sprintf(`
candidate-listen = "unix:/tmp/candidate.sock"
match-window = "2s"
scripts-dir = %q
descriptors-dir = %q

[destination]
"api.weather.example" = "https://api.weather.example"
"users.example" = "h2c://127.0.0.1:9000"
`, scripts, descriptors)), 0o600)
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
	expected.Schema.DescriptorsDir = descriptors
	expectedComparison := comparison.NewConfig()
	expectedComparison.ScriptsDir = scripts
	assert.Equal(t, expected, command.Egress)
	assert.Equal(t, expectedComparison, command.Comparison)
}

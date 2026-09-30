package main

import (
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/kong"

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

package main

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/ingress"
)

func TestCLIEmbedsIngressConfig(t *testing.T) {
	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
	})
	assert.NoError(t, err)
	expected := ingress.NewConfig()
	expected.Reference = "http://reference.example"
	expected.Candidate = "http://candidate.example"
	assert.Equal(t, expected, command.Config)
}

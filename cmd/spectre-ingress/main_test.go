package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/kong"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/ingress"
	"github.com/block/spectre/internal/logger"
)

func TestCheckSchemaCommandRun(t *testing.T) {
	data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name:   new("test.proto"),
		Syntax: new("proto3"),
	}}})
	assert.NoError(t, err)
	descriptorSet := filepath.Join(t.TempDir(), "test.pb")
	assert.NoError(t, os.WriteFile(descriptorSet, data, 0o600))

	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)
	kctx, err := parser.Parse([]string{"check-schema", descriptorSet})
	assert.NoError(t, err)
	var output bytes.Buffer
	log := logger.New(logger.NewConfig(), &output)
	assert.NoError(t, kctx.Run(&commandContext{ctx: t.Context(), log: log}))
	assert.Contains(t, output.String(), "Descriptor set is valid")
}

func TestServeCommandEmbedsIngressConfig(t *testing.T) {
	command := &cli{}
	parser, err := kong.New(command)
	assert.NoError(t, err)
	_, err = parser.Parse([]string{
		"serve",
		"--reference=http://reference.example",
		"--candidate=http://candidate.example",
	})
	assert.NoError(t, err)
	expected := ingress.NewConfig()
	expected.Reference = "http://reference.example"
	expected.Candidate = "http://candidate.example"
	assert.Equal(t, expected, command.Serve.Config)
}

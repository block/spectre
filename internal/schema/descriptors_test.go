package schema_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/schema"
)

func TestLoadsAndMergesDescriptorSetFiles(t *testing.T) {
	full := newDescriptorSet()
	service := &descriptorpb.FileDescriptorSet{File: full.GetFile()}
	// The second file repeats an identical import, as separate protoc runs would.
	messages := &descriptorpb.FileDescriptorSet{File: full.GetFile()[1:]}
	config := schema.NewConfig()
	config.DescriptorSets = []string{writeDescriptorSet(t, service), writeDescriptorSet(t, messages)}

	loaded, err := schema.LoadDescriptorSets(config)
	assert.NoError(t, err)

	expected := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{full.GetFile()[1], full.GetFile()[0]}}
	assert.True(t, proto.Equal(expected, loaded))
}

func TestLoadsNoDescriptorSetFiles(t *testing.T) {
	loaded, err := schema.LoadDescriptorSets(schema.NewConfig())
	assert.NoError(t, err)
	assert.True(t, proto.Equal(&descriptorpb.FileDescriptorSet{}, loaded))
}

func TestMergeIgnoresMetadataDifferences(t *testing.T) {
	first := newDescriptorSet()
	annotated := newDescriptorSet()
	// Buf images add source info and an unrecognised field to each file.
	for _, file := range annotated.GetFile() {
		file.SourceCodeInfo = &descriptorpb.SourceCodeInfo{Location: []*descriptorpb.SourceCodeInfo_Location{{Span: []int32{0, 0, 1}}}}
		file.ProtoReflect().SetUnknown([]byte("\xd2\xf6\x03\x04\x08\x01\x18\x00"))
	}

	merged, err := schema.Merge(first, annotated)
	assert.NoError(t, err)

	expected := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{first.GetFile()[1], first.GetFile()[0]}}
	assert.True(t, proto.Equal(expected, merged))
}

func TestRejectsInvalidDescriptorSetFiles(t *testing.T) {
	conflicting := newDescriptorSet()
	conflicting.File[1].MessageType[0].Name = new("OtherRequest")
	for name, test := range map[string]struct {
		files   func(t *testing.T) []string
		message string
	}{
		"Missing": {
			files:   func(t *testing.T) []string { return []string{filepath.Join(t.TempDir(), "missing.binpb")} },
			message: "read descriptor set",
		},
		"Malformed": {
			files: func(t *testing.T) []string {
				path := filepath.Join(t.TempDir(), "malformed.binpb")
				assert.NoError(t, os.WriteFile(path, []byte{0xff}, 0o600))
				return []string{path}
			},
			message: "decode descriptor set",
		},
		"MissingImport": {
			files: func(t *testing.T) []string {
				return []string{writeDescriptorSet(t, &descriptorpb.FileDescriptorSet{File: newDescriptorSet().GetFile()[:1]})}
			},
			message: "load descriptor sets",
		},
		"Conflict": {
			files: func(t *testing.T) []string {
				return []string{writeDescriptorSet(t, newDescriptorSet()), writeDescriptorSet(t, conflicting)}
			},
			message: `conflicting descriptors for file "messages.proto"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := schema.NewConfig()
			config.DescriptorSets = test.files(t)
			_, err := schema.LoadDescriptorSets(config)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func writeDescriptorSet(t *testing.T, set *descriptorpb.FileDescriptorSet) string {
	t.Helper()
	data, err := proto.Marshal(set)
	assert.NoError(t, err)
	path := filepath.Join(t.TempDir(), "descriptors.binpb")
	assert.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

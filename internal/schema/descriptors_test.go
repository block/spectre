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
	config.DescriptorsDir = writeDescriptorSets(t, map[string][]byte{
		"service.pb":         marshal(t, service),
		"nested/messages.pb": marshal(t, messages),
		"notes.txt":          []byte("not a descriptor set"),
	})

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
		files   map[string][]byte
		message string
	}{
		"NoFiles":       {files: map[string][]byte{"notes.txt": nil}, message: "contains no .pb files"},
		"Malformed":     {files: map[string][]byte{"malformed.pb": {0xff}}, message: `decode descriptor set "malformed.pb"`},
		"Empty":         {files: map[string][]byte{"empty.pb": marshal(t, &descriptorpb.FileDescriptorSet{})}, message: `descriptor set "empty.pb" contains no files`},
		"MissingImport": {files: map[string][]byte{"service.pb": marshal(t, &descriptorpb.FileDescriptorSet{File: newDescriptorSet().GetFile()[:1]})}, message: "load descriptor sets"},
		"Conflict": {
			files:   map[string][]byte{"a.pb": marshal(t, newDescriptorSet()), "b.pb": marshal(t, conflicting)},
			message: `conflicting descriptors for file "messages.proto"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := schema.NewConfig()
			config.DescriptorsDir = writeDescriptorSets(t, test.files)
			_, err := schema.LoadDescriptorSets(config)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestRejectsMissingDescriptorsDir(t *testing.T) {
	config := schema.NewConfig()
	config.DescriptorsDir = filepath.Join(t.TempDir(), "missing")
	_, err := schema.LoadDescriptorSets(config)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read descriptors directory")
}

func writeDescriptorSets(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		assert.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		assert.NoError(t, os.WriteFile(path, data, 0o600))
	}
	return dir
}

func marshal(t *testing.T, set *descriptorpb.FileDescriptorSet) []byte {
	t.Helper()
	data, err := proto.Marshal(set)
	assert.NoError(t, err)
	return data
}

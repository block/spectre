package schema

import (
	"os"
	"slices"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Config selects static schema sources.
type Config struct {
	// DescriptorSets are binary FileDescriptorSet files, each including its imports.
	DescriptorSets []string `name:"descriptor-set" type:"existingfile" sep:"none" placeholder:"FILE" help:"Binary protobuf FileDescriptorSet file, including imports. Repeatable."`
}

// NewConfig returns the default schema configuration.
func NewConfig() Config {
	config := Config{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply schema defaults"))
	}
	return config
}

// LoadDescriptorSets merges the configured descriptor set files into one set.
// The result is empty when no files are configured.
func LoadDescriptorSets(config Config) (*descriptorpb.FileDescriptorSet, error) {
	sets := make([]*descriptorpb.FileDescriptorSet, 0, len(config.DescriptorSets))
	for _, path := range config.DescriptorSets {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.Wrapf(err, "read descriptor set %q", path)
		}
		set := &descriptorpb.FileDescriptorSet{}
		if err := proto.Unmarshal(data, set); err != nil {
			return nil, errors.Wrapf(err, "decode descriptor set %q", path)
		}
		if len(set.GetFile()) == 0 {
			return nil, errors.Errorf("descriptor set %q contains no files", path)
		}
		sets = append(sets, set)
	}
	merged, err := Merge(sets...)
	if err != nil {
		return nil, errors.Wrap(err, "merge descriptor sets")
	}
	if len(merged.GetFile()) > 0 {
		// Resolve now so a missing import fails at startup rather than at readiness.
		if _, err := NewFromFileDescriptorSet(merged); err != nil {
			return nil, errors.Wrap(err, "load descriptor sets")
		}
	}
	return merged, nil
}

// Merge combines descriptor sets, sorted by file name. A file may appear in several
// sets if the copies differ only in metadata; the first copy is kept.
func Merge(sets ...*descriptorpb.FileDescriptorSet) (*descriptorpb.FileDescriptorSet, error) {
	files := map[string]*descriptorpb.FileDescriptorProto{}
	schemas := map[string]*descriptorpb.FileDescriptorProto{}
	for _, set := range sets {
		for _, file := range set.GetFile() {
			schema, err := withoutMetadata(file)
			if err != nil {
				return nil, errors.Wrapf(err, "compare descriptors for file %q", file.GetName())
			}
			if previous, ok := schemas[file.GetName()]; ok {
				if !proto.Equal(previous, schema) {
					return nil, errors.Errorf("conflicting descriptors for file %q", file.GetName())
				}
				continue
			}
			files[file.GetName()] = file
			schemas[file.GetName()] = schema
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	merged := &descriptorpb.FileDescriptorSet{File: make([]*descriptorpb.FileDescriptorProto, 0, len(names))}
	for _, name := range names {
		merged.File = append(merged.File, files[name])
	}
	return merged, nil
}

// withoutMetadata drops what never affects decoding: source info, and unrecognised
// fields such as Buf image metadata or unlinked custom options.
func withoutMetadata(file *descriptorpb.FileDescriptorProto) (*descriptorpb.FileDescriptorProto, error) {
	copied, ok := proto.Clone(file).(*descriptorpb.FileDescriptorProto)
	if !ok {
		return nil, errors.New("descriptor copy has an unexpected type")
	}
	copied.SourceCodeInfo = nil
	data, err := proto.Marshal(copied)
	if err != nil {
		return nil, errors.Wrap(err, "encode descriptor")
	}
	stripped := &descriptorpb.FileDescriptorProto{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, stripped); err != nil {
		return nil, errors.Wrap(err, "decode descriptor")
	}
	return stripped, nil
}

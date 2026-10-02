package descriptors

import (
	"io/fs"
	"os"
	"path"
	"slices"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Config selects static descriptor sources.
type Config struct {
	// DescriptorsDir holds binary FileDescriptorSet files, each including its imports.
	DescriptorsDir string `type:"existingdir" placeholder:"DIR" help:"Directory of binary protobuf FileDescriptorSet files, each including its imports. Every .pb file in it, including subdirectories, is loaded."`
}

// NewConfig returns the default descriptors configuration.
func NewConfig() Config {
	config := Config{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply descriptors defaults"))
	}
	return config
}

// LoadDescriptorSets merges every .pb file in the configured directory into one set.
// The result is empty when no directory is configured.
func LoadDescriptorSets(config Config) (*descriptorpb.FileDescriptorSet, error) {
	if config.DescriptorsDir == "" {
		return &descriptorpb.FileDescriptorSet{}, nil
	}
	descriptors := os.DirFS(config.DescriptorsDir)
	sets := []*descriptorpb.FileDescriptorSet{}
	// WalkDir visits files in lexical order, so merging is deterministic.
	err := fs.WalkDir(descriptors, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return errors.Wrap(err, "read descriptors directory")
		}
		if entry.IsDir() || path.Ext(name) != ".pb" {
			return nil
		}
		set, err := loadDescriptorSet(descriptors, name)
		if err != nil {
			return err
		}
		sets = append(sets, set)
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if len(sets) == 0 {
		return nil, errors.Errorf("descriptors directory %q contains no .pb files", config.DescriptorsDir)
	}
	merged, err := Merge(sets...)
	if err != nil {
		return nil, errors.Wrap(err, "merge descriptor sets")
	}
	// Resolve now so a missing import fails at startup rather than at readiness.
	if _, err := NewRegistry(merged); err != nil {
		return nil, errors.Wrap(err, "load descriptor sets")
	}
	return merged, nil
}

func loadDescriptorSet(descriptors fs.FS, name string) (*descriptorpb.FileDescriptorSet, error) {
	data, err := fs.ReadFile(descriptors, name)
	if err != nil {
		return nil, errors.Wrapf(err, "read descriptor set %q", name)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, set); err != nil {
		return nil, errors.Wrapf(err, "decode descriptor set %q", name)
	}
	if len(set.GetFile()) == 0 {
		return nil, errors.Errorf("descriptor set %q contains no files", name)
	}
	return set, nil
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

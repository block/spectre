// Package schema loads the protobuf descriptors used by ingress comparisons.
package schema

import (
	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Schema resolves messages and RPC methods from a local descriptor set.
type Schema struct {
	files *protoregistry.Files
	types *dynamicpb.Types
}

// New loads a binary FileDescriptorSet containing all of its imported files.
func New(data []byte) (*Schema, error) {
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, set); err != nil {
		return nil, errors.Wrap(err, "decode descriptor set")
	}
	return NewFromFileDescriptorSet(set)
}

// NewFromFileDescriptorSet loads a descriptor set containing all imported files.
func NewFromFileDescriptorSet(set *descriptorpb.FileDescriptorSet) (*Schema, error) {
	if set == nil {
		return nil, errors.New("descriptor set is nil")
	}
	if len(set.GetFile()) == 0 {
		return nil, errors.New("descriptor set contains no files")
	}
	// Resolve only against this set so process-global registrations cannot change the schema.
	files, err := protodesc.NewFiles(set)
	if err != nil {
		return nil, errors.Wrap(err, "resolve descriptor set")
	}
	return newSchema(files), nil
}

func newSchema(files *protoregistry.Files) *Schema {
	return &Schema{files: files, types: dynamicpb.NewTypes(files)}
}

// Types returns the dynamic types resolved from this schema.
func (s *Schema) Types() *dynamicpb.Types {
	return s.types
}

// Message resolves a fully qualified protobuf message name, including nested messages.
func (s *Schema) Message(name protoreflect.FullName) (protoreflect.MessageDescriptor, error) {
	descriptor, err := s.files.FindDescriptorByName(name)
	if err != nil {
		return nil, errors.Wrapf(err, "resolve message %q", name)
	}
	message, ok := descriptor.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, errors.Errorf("descriptor %q is not a message", name)
	}
	return message, nil
}

// Method resolves a fully qualified RPC name such as example.users.v1.UserService.ListUsers.
func (s *Schema) Method(name protoreflect.FullName) (protoreflect.MethodDescriptor, error) {
	descriptor, err := s.files.FindDescriptorByName(name)
	if err != nil {
		return nil, errors.Wrapf(err, "resolve method %q", name)
	}
	method, ok := descriptor.(protoreflect.MethodDescriptor)
	if !ok {
		return nil, errors.Errorf("descriptor %q is not a method", name)
	}
	return method, nil
}

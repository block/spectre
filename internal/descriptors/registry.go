// Package descriptors loads the protobuf descriptors that gRPC and Connect wire
// decoding uses.
package descriptors

import (
	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Registry resolves messages and RPC methods from a local descriptor set.
type Registry struct {
	files *protoregistry.Files
	types *dynamicpb.Types
}

// New loads a binary FileDescriptorSet containing all of its imported files.
func New(data []byte) (*Registry, error) {
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, set); err != nil {
		return nil, errors.Wrap(err, "decode descriptor set")
	}
	return NewRegistry(set)
}

// NewRegistry loads a descriptor set containing all imported files. An empty set
// is valid, as services that only serve raw HTTP need no descriptors.
func NewRegistry(set *descriptorpb.FileDescriptorSet) (*Registry, error) {
	if set == nil {
		return nil, errors.New("descriptor set is nil")
	}
	// Resolve only against this set so process-global registrations cannot change the schema.
	files, err := protodesc.NewFiles(set)
	if err != nil {
		return nil, errors.Wrap(err, "resolve descriptor set")
	}
	return &Registry{files: files, types: dynamicpb.NewTypes(files)}, nil
}

// Files returns the descriptors resolved from this registry.
func (r *Registry) Files() *protoregistry.Files {
	return r.files
}

// Types returns the dynamic types resolved from this registry.
func (r *Registry) Types() *dynamicpb.Types {
	return r.types
}

// Message resolves a fully qualified protobuf message name, including nested messages.
func (r *Registry) Message(name protoreflect.FullName) (protoreflect.MessageDescriptor, error) {
	descriptor, err := r.files.FindDescriptorByName(name)
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
func (r *Registry) Method(name protoreflect.FullName) (protoreflect.MethodDescriptor, error) {
	descriptor, err := r.files.FindDescriptorByName(name)
	if err != nil {
		return nil, errors.Wrapf(err, "resolve method %q", name)
	}
	method, ok := descriptor.(protoreflect.MethodDescriptor)
	if !ok {
		return nil, errors.Errorf("descriptor %q is not a method", name)
	}
	return method, nil
}

package schema_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/schema"
)

func TestResolveDescriptors(t *testing.T) {
	set := newDescriptorSet()
	data, err := proto.Marshal(set)
	assert.NoError(t, err)
	loaded, err := schema.New(data)
	assert.NoError(t, err)

	method, err := loaded.Method("example.users.v1.UserService.ListUsers")
	assert.NoError(t, err)
	// Protobuf equality ignores internal caches populated by marshaling the fixture.
	assert.True(t, proto.Equal(set.GetFile()[0].GetService()[0].GetMethod()[0], protodesc.ToMethodDescriptorProto(method)))
	assert.Equal(t, protoreflect.FullName("example.users.v1.ListUsersRequest"), method.Input().FullName())
	assert.Equal(t, protoreflect.FullName("example.users.v1.ListUsersResponse"), method.Output().FullName())

	message, err := loaded.Message("example.users.v1.ListUsersResponse")
	assert.NoError(t, err)
	assert.True(t, proto.Equal(set.GetFile()[1].GetMessageType()[1], protodesc.ToDescriptorProto(message)))
	// Method outputs and name lookups must resolve to the same descriptor within a schema.
	assert.True(t, method.Output() == message)

	nested, err := loaded.Message("example.users.v1.ListUsersResponse.User")
	assert.NoError(t, err)
	assert.True(t, proto.Equal(set.GetFile()[1].GetMessageType()[1].GetNestedType()[0], protodesc.ToDescriptorProto(nested)))
}

func TestRejectInvalidDescriptorSets(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*descriptorpb.FileDescriptorSet)
	}{
		{name: "Empty", mutate: func(set *descriptorpb.FileDescriptorSet) { set.File = nil }},
		{name: "MissingImport", mutate: func(set *descriptorpb.FileDescriptorSet) { set.File = set.GetFile()[:1] }},
		{name: "MissingGlobalImport", mutate: func(set *descriptorpb.FileDescriptorSet) {
			set.File[0].Dependency = append(set.GetFile()[0].GetDependency(), "google/protobuf/descriptor.proto")
		}},
		{name: "DuplicateFile", mutate: func(set *descriptorpb.FileDescriptorSet) {
			set.File = append(set.GetFile(), set.GetFile()[0])
		}},
		{name: "DuplicateSymbol", mutate: func(set *descriptorpb.FileDescriptorSet) {
			set.File[1].MessageType = append(set.GetFile()[1].GetMessageType(), set.GetFile()[1].GetMessageType()[0])
		}},
		{name: "UnknownMessage", mutate: func(set *descriptorpb.FileDescriptorSet) {
			set.File[0].Service[0].Method[0].OutputType = new(".example.users.v1.Unknown")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			set := newDescriptorSet()
			test.mutate(set)
			data, err := proto.Marshal(set)
			assert.NoError(t, err)
			loaded, err := schema.New(data)
			assert.Error(t, err)
			assert.Equal(t, (*schema.Schema)(nil), loaded)
		})
	}
}

func TestRejectMalformedDescriptorSet(t *testing.T) {
	loaded, err := schema.New([]byte{0xff})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode descriptor set")
	assert.Equal(t, (*schema.Schema)(nil), loaded)
}

func TestRejectInvalidLookups(t *testing.T) {
	data, err := proto.Marshal(newDescriptorSet())
	assert.NoError(t, err)
	loaded, err := schema.New(data)
	assert.NoError(t, err)

	for _, test := range []struct {
		name   string
		target protoreflect.FullName
	}{
		{name: "Empty"},
		{name: "Unknown", target: "example.users.v1.Unknown"},
		{name: "Service", target: "example.users.v1.UserService"},
		{name: "Field", target: "example.users.v1.ListUsersResponse.users"},
	} {
		t.Run(test.name, func(t *testing.T) {
			message, err := loaded.Message(test.target)
			assert.Error(t, err)
			assert.Equal(t, nil, message)
			method, err := loaded.Method(test.target)
			assert.Error(t, err)
			assert.Equal(t, nil, method)
		})
	}
	_, err = loaded.Message("example.users.v1.UserService.ListUsers")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "is not a message")
	_, err = loaded.Method("example.users.v1.ListUsersResponse")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "is not a method")
}

func TestSchemaIsolation(t *testing.T) {
	set := newDescriptorSet()
	data, err := proto.Marshal(set)
	assert.NoError(t, err)
	first, err := schema.New(data)
	assert.NoError(t, err)
	set.File[1].MessageType[1].NestedType[0].Field[0].JsonName = new("userID")
	data, err = proto.Marshal(set)
	assert.NoError(t, err)
	second, err := schema.New(data)
	assert.NoError(t, err)

	firstUser, err := first.Message("example.users.v1.ListUsersResponse.User")
	assert.NoError(t, err)
	secondUser, err := second.Message("example.users.v1.ListUsersResponse.User")
	assert.NoError(t, err)
	assert.Equal(t, "id", firstUser.Fields().Get(0).JSONName())
	assert.Equal(t, "userID", secondUser.Fields().Get(0).JSONName())
}

func newDescriptorSet() *descriptorpb.FileDescriptorSet {
	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		{
			Name:       new("service.proto"),
			Package:    new("example.users.v1"),
			Syntax:     new("proto3"),
			Dependency: []string{"messages.proto"},
			Service: []*descriptorpb.ServiceDescriptorProto{{
				Name: new("UserService"),
				Method: []*descriptorpb.MethodDescriptorProto{{
					Name:       new("ListUsers"),
					InputType:  new(".example.users.v1.ListUsersRequest"),
					OutputType: new(".example.users.v1.ListUsersResponse"),
				}},
			}},
		},
		{
			Name:    new("messages.proto"),
			Package: new("example.users.v1"),
			Syntax:  new("proto3"),
			MessageType: []*descriptorpb.DescriptorProto{
				{Name: new("ListUsersRequest")},
				{
					Name: new("ListUsersResponse"),
					Field: []*descriptorpb.FieldDescriptorProto{{
						Name:     new("users"),
						JsonName: new("users"),
						Number:   proto.Int32(1),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: new(".example.users.v1.ListUsersResponse.User"),
					}},
					NestedType: []*descriptorpb.DescriptorProto{{
						Name: new("User"),
						Field: []*descriptorpb.FieldDescriptorProto{{
							Name:     new("id"),
							JsonName: new("id"),
							Number:   proto.Int32(1),
							Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
							Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						}},
					}},
				},
			},
		},
	}}
}

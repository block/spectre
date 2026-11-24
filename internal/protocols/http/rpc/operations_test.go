package rpc_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/protocols/http/rpc"
	samplepb "github.com/block/spectre/internal/sample/pb"
	"github.com/block/spectre/internal/schema"
)

func TestOperationsReadMethods(t *testing.T) {
	operations := rpc.New([]schema.Method{{Interface: "example.Service", Name: "Get", Parameter: "example.Request", Result: "example.Response"}})
	operation, err := operations.Operation("example.Service.Get")
	assert.NoError(t, err)
	assert.Equal(t, rpc.Operation{Name: "example.Service.Get", Request: "example.Request", Response: "example.Response"}, operation)
	_, err = operations.Operation("example.Service.Missing")
	assert.EqualError(t, err, `operation "example.Service.Missing" is not declared`)
}

func TestCheckRequiresEveryUnaryMethod(t *testing.T) {
	set := sampleDescriptorSet()
	loaded := parseGenerated(t, set)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	assert.NoError(t, rpc.New(loaded.Methods()).Check(registry))
	for _, name := range []string{"GetUser", "ListUsers"} {
		t.Run(name, func(t *testing.T) {
			var methods []schema.Method
			for _, method := range loaded.Methods() {
				if method.Name != name {
					methods = append(methods, method)
				}
			}
			err := rpc.New(methods).Check(registry)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `protobuf method "spectre.sample.v1.UserService.`+name+`" needs a declaration`)
		})
	}
}

func TestCheckRejectsMismatchedTypes(t *testing.T) {
	set := sampleDescriptorSet()
	loaded := parseGenerated(t, set)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	for _, name := range []string{"Parameter", "Result"} {
		t.Run(name, func(t *testing.T) {
			methods := loaded.Methods()
			if name == "Parameter" {
				methods[0].Parameter = "spectre.sample.v1.User"
			} else {
				methods[0].Result = "spectre.sample.v1.User"
			}
			err := rpc.New(methods).Check(registry)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `operation "spectre.sample.v1.UserService.GetUser" has types`)
		})
	}
}

func TestCheckRejectsDeclaredStreamingMethods(t *testing.T) {
	set := streamingDescriptorSet()
	loaded := parseGenerated(t, set)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	assert.NoError(t, rpc.New(loaded.Methods()).Check(registry))
	for _, name := range []string{"ClientStream", "ServerStream", "BidirectionalStream"} {
		t.Run(name, func(t *testing.T) {
			methods := loaded.Methods()
			streaming := methods[0]
			streaming.Name = name
			err := rpc.New(append(methods, streaming)).Check(registry)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "is a streaming protobuf method")
		})
	}
}

func TestCheckRejectsScalarMethodTypes(t *testing.T) {
	for _, side := range []string{"Input", "Output"} {
		t.Run(side, func(t *testing.T) {
			registry, err := descriptors.NewRegistry(scalarMethodDescriptorSet(side))
			assert.NoError(t, err)
			err = rpc.New(nil).Check(registry)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `uses unsupported request or response type "google.protobuf.Timestamp"`)
		})
	}
}

func TestCheckAcceptsRawHTTPOperations(t *testing.T) {
	set := sampleDescriptorSet()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	generated["raw.d.ts"] = `declare module "raw" { interface RawRequest { id?: string } interface RawResponse { count: number } interface RawService { Fetch(request: RawRequest): RawResponse } }`
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	registry, err := descriptors.NewRegistry(set)
	assert.NoError(t, err)
	assert.NoError(t, rpc.New(loaded.Methods()).Check(registry))
	loaded, err = schema.ParseSources(t.Context(), map[string]string{"raw.d.ts": generated["raw.d.ts"]})
	assert.NoError(t, err)
	registry, err = descriptors.NewRegistry(&descriptorpb.FileDescriptorSet{})
	assert.NoError(t, err)
	assert.NoError(t, rpc.New(loaded.Methods()).Check(registry))
}

func parseGenerated(t *testing.T, set *descriptorpb.FileDescriptorSet) *schema.Schema {
	t.Helper()
	generated, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	loaded, err := schema.ParseSources(t.Context(), generated)
	assert.NoError(t, err)
	return loaded
}

func sampleDescriptorSet() *descriptorpb.FileDescriptorSet {
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var include func(file protoreflect.FileDescriptor)
	include = func(file protoreflect.FileDescriptor) {
		if seen[file.Path()] {
			return
		}
		seen[file.Path()] = true
		for index := range file.Imports().Len() {
			include(file.Imports().Get(index).FileDescriptor)
		}
		set.File = append(set.GetFile(), protodesc.ToFileDescriptorProto(file))
	}
	include(samplepb.File_users_proto)
	include(samplepb.File_service_proto)
	return set
}

func payloadFile() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name: new("payload.proto"), Package: new("example"), Syntax: new("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: new("Payload")}},
	}
}

func unaryMethod(name string) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{Name: new(name), InputType: new(".example.Payload"), OutputType: new(".example.Payload")}
}

// streamingDescriptorSet has one unary method and one of each streaming kind.
func streamingDescriptorSet() *descriptorpb.FileDescriptorSet {
	file := payloadFile()
	methods := []*descriptorpb.MethodDescriptorProto{unaryMethod("ListUsers")}
	for _, test := range []struct {
		name           string
		client, server bool
	}{
		{name: "ClientStream", client: true},
		{name: "ServerStream", server: true},
		{name: "BidirectionalStream", client: true, server: true},
	} {
		method := unaryMethod(test.name)
		method.ClientStreaming = new(test.client)
		method.ServerStreaming = new(test.server)
		methods = append(methods, method)
	}
	file.Service = []*descriptorpb.ServiceDescriptorProto{{Name: new("UserService"), Method: methods}}
	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{file}}
}

// scalarMethodDescriptorSet has a method whose input or output is a well-known scalar.
func scalarMethodDescriptorSet(side string) *descriptorpb.FileDescriptorSet {
	file := payloadFile()
	file.Dependency = []string{"google/protobuf/timestamp.proto"}
	method := unaryMethod("Get")
	if side == "Input" {
		method.InputType = new(".google.protobuf.Timestamp")
	} else {
		method.OutputType = new(".google.protobuf.Timestamp")
	}
	file.Service = []*descriptorpb.ServiceDescriptorProto{{Name: new("Service"), Method: []*descriptorpb.MethodDescriptorProto{method}}}
	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		protodesc.ToFileDescriptorProto(timestamppb.File_google_protobuf_timestamp_proto), file,
	}}
}

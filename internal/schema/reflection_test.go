package schema_test

import (
	"net"
	"testing"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"

	samplepb "github.com/block/spectre/internal/sample/pb"
	"github.com/block/spectre/internal/schema"
)

func TestReflectionLoaderLoadsApplicationDescriptors(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	server := grpc.NewServer()
	samplepb.RegisterUserServiceServer(server, &samplepb.UnimplementedUserServiceServer{})
	reflection.Register(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	set, err := schema.NewReflectionLoader().Load(t.Context(), "h2c://"+listener.Addr().String())
	assert.NoError(t, err)
	files, err := protodesc.NewFiles(set)
	assert.NoError(t, err)
	descriptor, err := files.FindDescriptorByName(protoreflect.FullName("spectre.sample.v1.UserService"))
	assert.NoError(t, err)
	_, ok := descriptor.(protoreflect.ServiceDescriptor)
	assert.True(t, ok)
	for index := 1; index < len(set.GetFile()); index++ {
		assert.True(t, set.GetFile()[index-1].GetName() < set.GetFile()[index].GetName())
	}
	_, err = files.FindDescriptorByName(protoreflect.FullName("grpc.reflection.v1.ServerReflection"))
	assert.Error(t, err)
}

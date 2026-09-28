package schema_test

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/grpcreflect"
	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/sample/pb/samplepbconnect"
	"github.com/block/spectre/internal/schema"
)

func TestReflectionLoaderLoadsApplicationDescriptors(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	serveReflection(t, listener)

	set, err := schema.NewReflectionLoader().Load(t.Context(), "h2c://"+listener.Addr().String())
	assert.NoError(t, err)
	assertUserServiceDescriptors(t, set)
}

func TestReflectionLoaderLoadsOverUnixSocket(t *testing.T) {
	socket := filepath.Join(shortSocketDir(t), "reflection.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	assert.NoError(t, err)
	serveReflection(t, listener)

	set, err := schema.NewReflectionLoader().Load(t.Context(), "h2c+unix:"+socket)
	assert.NoError(t, err)
	assertUserServiceDescriptors(t, set)
}

// serveReflection runs a sample UserService with reflection on listener.
func serveReflection(t *testing.T, listener net.Listener) {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := samplepbconnect.NewUserServiceHandler(samplepbconnect.UnimplementedUserServiceHandler{})
	mux.Handle(path, handler)
	reflector := grpcreflect.NewStaticReflector(samplepbconnect.UserServiceName)
	path, handler = grpcreflect.NewHandlerV1(reflector)
	mux.Handle(path, handler)
	path, handler = grpcreflect.NewHandlerV1Alpha(reflector)
	mux.Handle(path, handler)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{Handler: mux, Protocols: protocols}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { assert.NoError(t, server.Close()) })
}

// assertUserServiceDescriptors checks the sample service is present and the
// reflection namespace is excluded, with files in stable name order.
func assertUserServiceDescriptors(t *testing.T, set *descriptorpb.FileDescriptorSet) {
	t.Helper()
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

// shortSocketDir returns a temporary directory with a short path so unix socket
// names stay within the operating system's limit.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	// A short base path keeps unix socket names within the 104-byte OS limit.
	dir, err := os.MkdirTemp("/tmp", "spectre") //nolint:usetesting // t.TempDir's base path is too long for unix sockets.
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(dir)) })
	return dir
}

package schema

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

const (
	schemeHTTP  = "http"
	schemeH2C   = "h2c"
	networkUnix = "unix"
)

// ReflectionLoader loads service descriptors from a gRPC reflection endpoint.
type ReflectionLoader struct{}

// NewReflectionLoader constructs a gRPC reflection descriptor loader.
func NewReflectionLoader() *ReflectionLoader {
	return &ReflectionLoader{}
}

// Load returns the descriptor set exposed by the endpoint's application services.
func (loader *ReflectionLoader) Load(ctx context.Context, endpoint string) (*descriptorpb.FileDescriptorSet, error) {
	client, closeClient, err := reflectionClient(endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "configure reflection client")
	}
	defer closeClient()

	stream := client.NewStream(ctx)
	defer func() { _, _ = stream.Close() }()
	services, err := stream.ListServices()
	if err != nil {
		return nil, errors.Wrap(err, "list reflected services")
	}

	serviceNames := make([]protoreflect.FullName, 0, len(services))
	for _, service := range services {
		// Reflection services are control-plane endpoints, not application schema.
		if !strings.HasPrefix(string(service), "grpc.reflection.") {
			serviceNames = append(serviceNames, service)
		}
	}
	slices.Sort(serviceNames)
	files, err := loadServiceFiles(stream, serviceNames)
	if err != nil {
		return nil, errors.Wrap(err, "load reflected service files")
	}

	fileNames := make([]string, 0, len(files))
	for name := range files {
		fileNames = append(fileNames, name)
	}
	// Stable ordering makes schema equality independent of reflection response order.
	slices.Sort(fileNames)
	set := &descriptorpb.FileDescriptorSet{File: make([]*descriptorpb.FileDescriptorProto, 0, len(files))}
	for _, name := range fileNames {
		set.File = append(set.File, files[name])
	}
	return set, nil
}

func loadServiceFiles(
	stream *grpcreflect.ClientStream,
	serviceNames []protoreflect.FullName,
) (map[string]*descriptorpb.FileDescriptorProto, error) {
	files := map[string]*descriptorpb.FileDescriptorProto{}
	for _, serviceName := range serviceNames {
		descriptors, err := stream.FileContainingSymbol(serviceName)
		if err != nil {
			return nil, errors.Wrapf(err, "load descriptors for service %q", serviceName)
		}
		for _, file := range descriptors {
			if file.GetName() == "" {
				return nil, errors.Errorf("reflected descriptor for service %q has no file name", serviceName)
			}
			// Dependencies may recur, but one file identity must never have two definitions.
			if previous, ok := files[file.GetName()]; ok && !proto.Equal(previous, file) {
				return nil, errors.Errorf("reflection returned conflicting descriptors for file %q", file.GetName())
			}
			files[file.GetName()] = file
		}
	}
	return files, nil
}

func reflectionClient(endpoint string) (*grpcreflect.Client, func(), error) {
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	transport := &http.Transport{Protocols: protocols}

	if scheme, socket, ok := splitUnixEndpoint(endpoint); ok {
		if !strings.HasPrefix(socket, "/") && !strings.HasPrefix(socket, "@") {
			return nil, nil, errors.Errorf("unix reflection socket must be an absolute path or an abstract name beginning with @: %q", endpoint)
		}
		if scheme == schemeH2C {
			protocols.SetUnencryptedHTTP2(true)
		}
		dialer := &net.Dialer{}
		transport.DialContext = func(ctx context.Context, _ string, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, networkUnix, socket)
		}
		return newReflectionClient(transport, "http://localhost"), transport.CloseIdleConnections, nil
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, nil, errors.Wrap(err, "parse reflection endpoint")
	}
	if parsed.Host == "" {
		return nil, nil, errors.Errorf("reflection endpoint must be absolute: %q", endpoint)
	}
	switch parsed.Scheme {
	case schemeHTTP, schemeH2C:
		parsed.Scheme = schemeHTTP
		protocols.SetUnencryptedHTTP2(true)
	case "https":
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	default:
		return nil, nil, errors.Errorf("reflection endpoint must use http, https, or h2c: %q", endpoint)
	}
	return newReflectionClient(transport, parsed.String()), transport.CloseIdleConnections, nil
}

// splitUnixEndpoint reports the scheme and socket for the "<scheme>+unix:" forms.
// ok is false when endpoint uses no unix scheme.
func splitUnixEndpoint(endpoint string) (scheme string, socket string, ok bool) {
	for _, candidate := range []string{schemeHTTP, schemeH2C} {
		if trimmed, found := strings.CutPrefix(endpoint, candidate+"+unix:"); found {
			return candidate, trimmed, true
		}
	}
	return "", "", false
}

func newReflectionClient(transport *http.Transport, baseURL string) *grpcreflect.Client {
	httpClient := &http.Client{Transport: transport}
	return grpcreflect.NewClient(httpClient, baseURL, connect.WithGRPC())
}

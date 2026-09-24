package schema

import (
	"context"
	"crypto/tls"
	"net/url"
	"sort"
	"strings"

	"github.com/alecthomas/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// ReflectionLoader loads service descriptors from a gRPC reflection endpoint.
type ReflectionLoader struct{}

// NewReflectionLoader constructs a gRPC reflection descriptor loader.
func NewReflectionLoader() *ReflectionLoader {
	return &ReflectionLoader{}
}

// Load returns the descriptor set exposed by the endpoint's application services.
func (loader *ReflectionLoader) Load(ctx context.Context, endpoint string) (*descriptorpb.FileDescriptorSet, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "parse reflection endpoint")
	}
	if parsed.Host == "" {
		return nil, errors.Errorf("reflection endpoint must be absolute: %q", endpoint)
	}
	transportCredentials, err := reflectionCredentials(parsed)
	if err != nil {
		return nil, errors.Wrap(err, "configure reflection credentials")
	}
	connection, err := grpc.NewClient(parsed.Host, grpc.WithTransportCredentials(transportCredentials))
	if err != nil {
		return nil, errors.Wrap(err, "create reflection client")
	}
	defer func() { _ = connection.Close() }()

	stream, err := reflectionpb.NewServerReflectionClient(connection).ServerReflectionInfo(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "open reflection stream")
	}
	response, err := reflectionRequest(stream, &reflectionpb.ServerReflectionRequest{
		MessageRequest: &reflectionpb.ServerReflectionRequest_ListServices{ListServices: ""},
	})
	if err != nil {
		return nil, errors.Wrap(err, "list reflected services")
	}
	services := response.GetListServicesResponse()
	if services == nil {
		return nil, errors.New("reflection service list response is missing")
	}

	serviceNames := make([]string, 0, len(services.GetService()))
	for _, service := range services.GetService() {
		if !strings.HasPrefix(service.GetName(), "grpc.reflection.") {
			serviceNames = append(serviceNames, service.GetName())
		}
	}
	sort.Strings(serviceNames)
	files, err := loadServiceFiles(stream, serviceNames)
	if err != nil {
		return nil, errors.Wrap(err, "load reflected service files")
	}

	fileNames := make([]string, 0, len(files))
	for name := range files {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)
	set := &descriptorpb.FileDescriptorSet{File: make([]*descriptorpb.FileDescriptorProto, 0, len(files))}
	for _, name := range fileNames {
		set.File = append(set.File, files[name])
	}
	return set, nil
}

func loadServiceFiles(
	stream reflectionpb.ServerReflection_ServerReflectionInfoClient,
	serviceNames []string,
) (map[string]*descriptorpb.FileDescriptorProto, error) {
	files := map[string]*descriptorpb.FileDescriptorProto{}
	for _, serviceName := range serviceNames {
		serviceResponse, err := reflectionRequest(stream, &reflectionpb.ServerReflectionRequest{
			MessageRequest: &reflectionpb.ServerReflectionRequest_FileContainingSymbol{FileContainingSymbol: serviceName},
		})
		if err != nil {
			return nil, errors.Wrapf(err, "load descriptors for service %q", serviceName)
		}
		descriptors := serviceResponse.GetFileDescriptorResponse()
		if descriptors == nil {
			return nil, errors.Errorf("reflection descriptor response for service %q is missing", serviceName)
		}
		for _, data := range descriptors.GetFileDescriptorProto() {
			file := &descriptorpb.FileDescriptorProto{}
			if err := proto.Unmarshal(data, file); err != nil {
				return nil, errors.Wrapf(err, "decode reflected descriptor for service %q", serviceName)
			}
			if file.GetName() == "" {
				return nil, errors.Errorf("reflected descriptor for service %q has no file name", serviceName)
			}
			if previous, ok := files[file.GetName()]; ok && !proto.Equal(previous, file) {
				return nil, errors.Errorf("reflection returned conflicting descriptors for file %q", file.GetName())
			}
			files[file.GetName()] = file
		}
	}
	return files, nil
}

func reflectionCredentials(endpoint *url.URL) (credentials.TransportCredentials, error) {
	switch endpoint.Scheme {
	case "http", "h2c":
		return insecure.NewCredentials(), nil
	case "https":
		return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12}), nil
	default:
		return nil, errors.Errorf("reflection endpoint must use http, https, or h2c: %q", endpoint.String())
	}
}

func reflectionRequest(
	stream reflectionpb.ServerReflection_ServerReflectionInfoClient,
	request *reflectionpb.ServerReflectionRequest,
) (*reflectionpb.ServerReflectionResponse, error) {
	if err := stream.Send(request); err != nil {
		return nil, errors.Wrap(err, "send reflection request")
	}
	response, err := stream.Recv()
	if err != nil {
		return nil, errors.Wrap(err, "receive reflection response")
	}
	if reflectionError := response.GetErrorResponse(); reflectionError != nil {
		return nil, errors.Errorf(
			"reflection request failed with code %d: %s",
			reflectionError.GetErrorCode(),
			reflectionError.GetErrorMessage(),
		)
	}
	return response, nil
}

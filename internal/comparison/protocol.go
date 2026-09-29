package comparison

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/block/spectre/internal/schema"
)

// protocol identifies the wire decoder used to produce one canonical JSON model.
type protocol int

const (
	protocolConnectJSON protocol = iota + 1
	protocolGRPC
	// protocolHTTPJSON is a raw HTTP response whose JSON body is the endpoint method's output.
	protocolHTTPJSON
)

const jsonMediaType = "application/json"

func excludedRequestPath(path string) bool {
	// Reflection and other gRPC control services are not application schema targets.
	return strings.HasPrefix(strings.Trim(path, "/"), "grpc.")
}

// requestProtocol selects the RPC wire protocol from the request content type.
func requestProtocol(contentType string) (protocol, Result) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return 0, Resultf(Skipped, "request content type is not supported: %v", err)
	}
	switch mediaType {
	case jsonMediaType:
		return protocolConnectJSON, newEmptyResult()
	case "application/grpc", "application/grpc+proto":
		return protocolGRPC, newEmptyResult()
	default:
		return 0, Resultf(Skipped, "request protocol is not supported: %q", mediaType)
	}
}

// conventionalMethod follows the gRPC and Connect convention of naming the
// method in the last two path segments.
func conventionalMethod(loaded *schema.Schema, path string) (protoreflect.MethodDescriptor, Result) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return nil, Resultf(Unable, "RPC path does not identify a method: %q", path)
	}
	name := protoreflect.FullName(parts[len(parts)-2] + "." + parts[len(parts)-1])
	method, err := loaded.Method(name)
	if err != nil {
		return nil, Resultf(Unable, "RPC method is absent from the comparison schema: %v", err)
	}
	if method.IsStreamingClient() || method.IsStreamingServer() {
		return nil, Resultf(Skipped, "streaming RPC comparison is not supported: %s", method.FullName())
	}
	return method, newEmptyResult()
}

// normaliseResponses creates the shared JSON representation consumed by JavaScript.
func normaliseResponses(
	loaded *schema.Schema,
	root protoreflect.MessageDescriptor,
	selected protocol,
	reference Response,
	candidate Response,
	maxResponseBytes int,
) ([]byte, []byte, Result) {
	if reference.StatusCode != candidate.StatusCode {
		return nil, nil, NewDifferenceResult("$status")
	}
	switch selected {
	case protocolConnectJSON:
		return normaliseConnect(loaded, root, reference, candidate)
	case protocolGRPC:
		return normaliseGRPC(loaded, root, reference, candidate, maxResponseBytes)
	case protocolHTTPJSON:
		return normaliseHTTPJSON(loaded, root, reference, candidate)
	default:
		return nil, nil, Resultf(Unable, "unknown comparison protocol: %d", selected)
	}
}

func normaliseConnect(
	loaded *schema.Schema,
	root protoreflect.MessageDescriptor,
	reference Response,
	candidate Response,
) ([]byte, []byte, Result) {
	if !hasJSONMediaType(reference.Header) || !hasJSONMediaType(candidate.Header) {
		return nil, nil, Resultf(
			Unable,
			"Connect response is not JSON: reference=%q candidate=%q",
			reference.Header.Get("Content-Type"),
			candidate.Header.Get("Content-Type"),
		)
	}
	if reference.StatusCode != http.StatusOK {
		referenceJSON, err := connectErrorJSON(reference.Body)
		if err != nil {
			return nil, nil, Resultf(Unable, "reference Connect error response contains malformed JSON: %v", err)
		}
		candidateJSON, err := connectErrorJSON(candidate.Body)
		if err != nil {
			return nil, nil, Resultf(Unable, "candidate Connect error response contains malformed JSON: %v", err)
		}
		return referenceJSON, candidateJSON, newEmptyResult()
	}
	referenceJSON, err := normaliseProtoJSON(loaded, root, reference.Body)
	if err != nil {
		return nil, nil, Resultf(Unable, "reference response is not valid ProtoJSON: %v", err)
	}
	candidateJSON, err := normaliseProtoJSON(loaded, root, candidate.Body)
	if err != nil {
		return nil, nil, Resultf(Unable, "candidate response is not valid ProtoJSON: %v", err)
	}
	return referenceJSON, candidateJSON, newEmptyResult()
}

// normaliseHTTPJSON decodes every non-empty body as the method output, so error
// bodies must be modelled in the same message as successful ones.
func normaliseHTTPJSON(
	loaded *schema.Schema,
	root protoreflect.MessageDescriptor,
	reference Response,
	candidate Response,
) ([]byte, []byte, Result) {
	// HEAD, 204, and 304 responses have no body to decode.
	referenceEmpty, candidateEmpty := len(reference.Body) == 0, len(candidate.Body) == 0
	if referenceEmpty && candidateEmpty {
		return nil, nil, Resultf(Equivalent, "")
	}
	if referenceEmpty != candidateEmpty {
		return nil, nil, NewDifferenceResult("$")
	}
	if !hasJSONMediaType(reference.Header) || !hasJSONMediaType(candidate.Header) {
		return nil, nil, Resultf(
			Unable,
			"HTTP response is not JSON: reference=%q candidate=%q",
			reference.Header.Get("Content-Type"),
			candidate.Header.Get("Content-Type"),
		)
	}
	referenceJSON, err := normaliseProtoJSON(loaded, root, reference.Body)
	if err != nil {
		return nil, nil, Resultf(Unable, "reference response does not match %s: %v", root.FullName(), err)
	}
	candidateJSON, err := normaliseProtoJSON(loaded, root, candidate.Body)
	if err != nil {
		return nil, nil, Resultf(Unable, "candidate response does not match %s: %v", root.FullName(), err)
	}
	return referenceJSON, candidateJSON, newEmptyResult()
}

func connectErrorJSON(body []byte) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return []byte("null"), nil
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, errors.Wrap(err, "decode Connect error JSON")
	}
	return body, nil
}

func normaliseGRPC(
	loaded *schema.Schema,
	root protoreflect.MessageDescriptor,
	reference Response,
	candidate Response,
	maxResponseBytes int,
) ([]byte, []byte, Result) {
	if reference.StatusCode != http.StatusOK {
		return nil, nil, Resultf(Unable, "gRPC response has HTTP status %d", reference.StatusCode)
	}
	if !hasGRPCMediaType(reference.Header) || !hasGRPCMediaType(candidate.Header) {
		return nil, nil, Resultf(
			Unable,
			"gRPC response has an invalid content type: reference=%q candidate=%q",
			reference.Header.Get("Content-Type"),
			candidate.Header.Get("Content-Type"),
		)
	}
	referenceStatus, err := grpcStatus(reference.Header)
	if err != nil {
		return nil, nil, Resultf(Unable, "reference response has no valid gRPC status: %v", err)
	}
	candidateStatus, err := grpcStatus(candidate.Header)
	if err != nil {
		return nil, nil, Resultf(Unable, "candidate response has no valid gRPC status: %v", err)
	}
	if referenceStatus != candidateStatus {
		return nil, nil, NewDifferenceResult("$status")
	}
	if referenceStatus != 0 {
		return nil, nil, Resultf(Equivalent, "")
	}
	referencePayload, err := grpcPayload(reference, maxResponseBytes)
	if err != nil {
		return nil, nil, Resultf(Unable, "reference response has invalid gRPC framing: %v", err)
	}
	candidatePayload, err := grpcPayload(candidate, maxResponseBytes)
	if err != nil {
		return nil, nil, Resultf(Unable, "candidate response has invalid gRPC framing: %v", err)
	}
	referenceJSON, err := normaliseProtoBinary(loaded, root, referencePayload)
	if err != nil {
		return nil, nil, Resultf(Unable, "reference response is not valid protobuf: %v", err)
	}
	candidateJSON, err := normaliseProtoBinary(loaded, root, candidatePayload)
	if err != nil {
		return nil, nil, Resultf(Unable, "candidate response is not valid protobuf: %v", err)
	}
	return referenceJSON, candidateJSON, newEmptyResult()
}

func hasJSONMediaType(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && mediaType == jsonMediaType
}

func hasGRPCMediaType(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && (mediaType == "application/grpc" || mediaType == "application/grpc+proto")
}

func grpcStatus(header http.Header) (int, error) {
	value := header.Get("Grpc-Status")
	if value == "" {
		value = header.Get(http.TrailerPrefix + "Grpc-Status")
	}
	status, err := strconv.Atoi(value)
	if err != nil {
		return 0, errors.Wrap(err, "parse gRPC status")
	}
	if status < 0 {
		return 0, errors.Errorf("gRPC status is negative: %d", status)
	}
	return status, nil
}

// grpcPayload accepts exactly one unary message and applies the limit after decompression.
func grpcPayload(response Response, maxResponseBytes int) ([]byte, error) {
	if len(response.Body) < 5 {
		return nil, errors.New("missing gRPC message frame")
	}
	length := int(binary.BigEndian.Uint32(response.Body[1:5]))
	if length != len(response.Body)-5 {
		return nil, errors.New("gRPC response must contain exactly one message")
	}
	payload := response.Body[5:]
	switch response.Body[0] {
	case 0:
		return payload, nil
	case 1:
		if response.Header.Get("Grpc-Encoding") != "gzip" {
			return nil, errors.New("unsupported gRPC compression")
		}
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, errors.Wrap(err, "open gzip response")
		}
		decompressed, err := io.ReadAll(io.LimitReader(reader, int64(maxResponseBytes)+1))
		closeErr := reader.Close()
		if err != nil {
			return nil, errors.Wrap(err, "decompress gRPC response")
		}
		if closeErr != nil {
			return nil, errors.Wrap(closeErr, "close gzip response")
		}
		if len(decompressed) > maxResponseBytes {
			return nil, errors.New("decompressed gRPC response exceeds the size limit")
		}
		return decompressed, nil
	default:
		return nil, errors.New("invalid gRPC compression flag")
	}
}

func normaliseProtoJSON(loaded *schema.Schema, descriptor protoreflect.MessageDescriptor, data []byte) ([]byte, error) {
	message := dynamicpb.NewMessage(descriptor)
	if err := (protojson.UnmarshalOptions{Resolver: loaded.Types()}).Unmarshal(data, message); err != nil {
		return nil, errors.Wrap(err, "decode ProtoJSON")
	}
	return marshalProtoJSON(loaded, message)
}

func normaliseProtoBinary(loaded *schema.Schema, descriptor protoreflect.MessageDescriptor, data []byte) ([]byte, error) {
	message := dynamicpb.NewMessage(descriptor)
	if err := (proto.UnmarshalOptions{Resolver: loaded.Types()}).Unmarshal(data, message); err != nil {
		return nil, errors.Wrap(err, "decode protobuf")
	}
	// ProtoJSON would silently discard unknown wire data and could hide divergence.
	if containsUnknown(message.ProtoReflect(), loaded.Types()) {
		return nil, errors.New("protobuf response contains unknown fields")
	}
	return marshalProtoJSON(loaded, message)
}

func marshalProtoJSON(loaded *schema.Schema, message proto.Message) ([]byte, error) {
	data, err := (protojson.MarshalOptions{Resolver: loaded.Types()}).Marshal(message)
	return data, errors.Wrap(err, "encode ProtoJSON")
}

func containsUnknown(message protoreflect.Message, types *dynamicpb.Types) bool {
	if len(message.GetUnknown()) > 0 {
		return true
	}
	if message.Descriptor().FullName() == "google.protobuf.Any" && anyContainsUnknown(message, types) {
		return true
	}
	unknown := false
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.IsMap():
			if field.MapValue().Kind() == protoreflect.MessageKind {
				value.Map().Range(func(_ protoreflect.MapKey, item protoreflect.Value) bool {
					unknown = containsUnknown(item.Message(), types)
					return !unknown
				})
			}
		case field.IsList():
			if field.Kind() == protoreflect.MessageKind {
				list := value.List()
				for index := range list.Len() {
					if containsUnknown(list.Get(index).Message(), types) {
						unknown = true
						break
					}
				}
			}
		case field.Kind() == protoreflect.MessageKind:
			unknown = containsUnknown(value.Message(), types)
		}
		return !unknown
	})
	return unknown
}

func anyContainsUnknown(message protoreflect.Message, types *dynamicpb.Types) bool {
	fields := message.Descriptor().Fields()
	typeURLField := fields.ByName("type_url")
	valueField := fields.ByName("value")
	if typeURLField == nil || valueField == nil || !message.Has(typeURLField) {
		return false
	}
	messageType, err := types.FindMessageByURL(message.Get(typeURLField).String())
	if err != nil {
		return true
	}
	embedded := messageType.New().Interface()
	if err := (proto.UnmarshalOptions{Resolver: types}).Unmarshal(message.Get(valueField).Bytes(), embedded); err != nil {
		return true
	}
	return containsUnknown(embedded.ProtoReflect(), types)
}

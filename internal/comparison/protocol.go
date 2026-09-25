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
)

func excludedRequestPath(path string) bool {
	// Reflection and other gRPC control services are not application schema targets.
	return strings.HasPrefix(strings.Trim(path, "/"), "grpc.")
}

func resolveMethod(loaded *schema.Schema, path, contentType string) (protoreflect.MethodDescriptor, protocol, Result) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, 0, Resultf(Skipped, "request content type is not supported: %v", err)
	}
	var selected protocol
	switch mediaType {
	case "application/json":
		selected = protocolConnectJSON
	case "application/grpc", "application/grpc+proto":
		selected = protocolGRPC
	default:
		return nil, 0, Resultf(Skipped, "request protocol is not supported: %q", mediaType)
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return nil, 0, Resultf(Unable, "RPC path does not identify a method: %q", path)
	}
	name := protoreflect.FullName(parts[len(parts)-2] + "." + parts[len(parts)-1])
	method, err := loaded.Method(name)
	if err != nil {
		return nil, 0, Resultf(Unable, "RPC method is absent from the comparison schema: %v", err)
	}
	if method.IsStreamingClient() || method.IsStreamingServer() {
		return nil, 0, Resultf(Skipped, "streaming RPC comparison is not supported: %s", method.FullName())
	}
	return method, selected, newEmptyResult()
}

// normaliseResponses creates the shared JSON representation consumed by JavaScript.
func normaliseResponses(
	loaded *schema.Schema,
	method protoreflect.MethodDescriptor,
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
		return normaliseConnect(loaded, method, reference, candidate)
	case protocolGRPC:
		return normaliseGRPC(loaded, method, reference, candidate, maxResponseBytes)
	default:
		return nil, nil, Resultf(Unable, "unknown comparison protocol: %d", selected)
	}
}

func normaliseConnect(
	loaded *schema.Schema,
	method protoreflect.MethodDescriptor,
	reference Response,
	candidate Response,
) ([]byte, []byte, Result) {
	if !hasMediaType(reference.Header, "application/json") || !hasMediaType(candidate.Header, "application/json") {
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
	referenceJSON, err := normaliseProtoJSON(loaded, method.Output(), reference.Body)
	if err != nil {
		return nil, nil, Resultf(Unable, "reference response is not valid ProtoJSON: %v", err)
	}
	candidateJSON, err := normaliseProtoJSON(loaded, method.Output(), candidate.Body)
	if err != nil {
		return nil, nil, Resultf(Unable, "candidate response is not valid ProtoJSON: %v", err)
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
	method protoreflect.MethodDescriptor,
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
	referenceJSON, err := normaliseProtoBinary(loaded, method.Output(), referencePayload)
	if err != nil {
		return nil, nil, Resultf(Unable, "reference response is not valid protobuf: %v", err)
	}
	candidateJSON, err := normaliseProtoBinary(loaded, method.Output(), candidatePayload)
	if err != nil {
		return nil, nil, Resultf(Unable, "candidate response is not valid protobuf: %v", err)
	}
	return referenceJSON, candidateJSON, newEmptyResult()
}

func hasMediaType(header http.Header, expected string) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && mediaType == expected
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

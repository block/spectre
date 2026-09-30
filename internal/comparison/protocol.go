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

// normaliseResponses decodes both responses to the JSON values that normalisers receive.
func normaliseResponses(
	loaded *schema.Schema,
	root protoreflect.MessageDescriptor,
	selected protocol,
	reference Response,
	candidate Response,
	maxResponseBytes int,
) (referencePayload, candidatePayload any, result Result) {
	if reference.StatusCode != candidate.StatusCode {
		return nil, nil, NewDifferenceResult("$status")
	}
	switch selected {
	case protocolConnectJSON:
		return normaliseConnect(loaded, root, reference, candidate, maxResponseBytes)
	case protocolGRPC:
		return normaliseGRPC(loaded, root, reference, candidate, maxResponseBytes)
	case protocolHTTPJSON:
		return normaliseHTTPJSON(loaded, root, reference, candidate, maxResponseBytes)
	default:
		return nil, nil, Resultf(Unable, "unknown comparison protocol: %d", selected)
	}
}

func normaliseConnect(
	loaded *schema.Schema,
	root protoreflect.MessageDescriptor,
	reference Response,
	candidate Response,
	maxResponseBytes int,
) (referencePayload, candidatePayload any, result Result) {
	if !hasJSONMediaType(reference.Header) || !hasJSONMediaType(candidate.Header) {
		return nil, nil, Resultf(
			Unable,
			"Connect response is not JSON: reference=%q candidate=%q",
			reference.Header.Get("Content-Type"),
			candidate.Header.Get("Content-Type"),
		)
	}
	if reference.StatusCode != http.StatusOK {
		referenceError, err := connectErrorPayload(reference.Body)
		if err != nil {
			return nil, nil, Resultf(Unable, "reference Connect error response contains malformed JSON: %v", err)
		}
		candidateError, err := connectErrorPayload(candidate.Body)
		if err != nil {
			return nil, nil, Resultf(Unable, "candidate Connect error response contains malformed JSON: %v", err)
		}
		return referenceError, candidateError, newEmptyResult()
	}
	return decodeResponses(loaded, root, protocolConnectJSON, reference, candidate, maxResponseBytes)
}

// normaliseHTTPJSON decodes every non-empty body as the method output, so error
// bodies must be modelled in the same message as successful ones.
func normaliseHTTPJSON(
	loaded *schema.Schema,
	root protoreflect.MessageDescriptor,
	reference Response,
	candidate Response,
	maxResponseBytes int,
) (referencePayload, candidatePayload any, result Result) {
	// HEAD, 204, and 304 responses have no body to decode.
	referenceEmpty, candidateEmpty := len(reference.Body) == 0, len(candidate.Body) == 0
	if referenceEmpty && candidateEmpty {
		return nil, nil, Resultf(Equivalent, "")
	}
	if referenceEmpty != candidateEmpty {
		return nil, nil, NewDifferenceResult("$")
	}
	return decodeResponses(loaded, root, protocolHTTPJSON, reference, candidate, maxResponseBytes)
}

func connectErrorPayload(body []byte) (any, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("null")
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.Wrap(err, "decode Connect error JSON")
	}
	return payload, nil
}

func normaliseGRPC(
	loaded *schema.Schema,
	root protoreflect.MessageDescriptor,
	reference Response,
	candidate Response,
	maxResponseBytes int,
) (referencePayload, candidatePayload any, result Result) {
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
	return decodeResponses(loaded, root, protocolGRPC, reference, candidate, maxResponseBytes)
}

// decodeResponses decodes both response bodies as root, reporting which side failed.
func decodeResponses(
	loaded *schema.Schema,
	root protoreflect.MessageDescriptor,
	selected protocol,
	reference Response,
	candidate Response,
	maxResponseBytes int,
) (referencePayload, candidatePayload any, result Result) {
	referencePayload, err := decodeResponse(loaded, root, selected, reference, maxResponseBytes)
	if err != nil {
		return nil, nil, Resultf(Unable, "cannot decode reference response as %s: %v", root.FullName(), err)
	}
	candidatePayload, err = decodeResponse(loaded, root, selected, candidate, maxResponseBytes)
	if err != nil {
		return nil, nil, Resultf(Unable, "cannot decode candidate response as %s: %v", root.FullName(), err)
	}
	return referencePayload, candidatePayload, newEmptyResult()
}

// decodeResponse decodes the body as the method output.
func decodeResponse(
	loaded *schema.Schema,
	output protoreflect.MessageDescriptor,
	selected protocol,
	response Response,
	maxBodyBytes int,
) (any, error) {
	message, err := decodePayload(loaded, output, selected, response.Header, response.Body, maxBodyBytes)
	if err != nil {
		return nil, err
	}
	return payloadValue(loaded, message, maxBodyBytes)
}

// decodeRequest decodes the body as the method input, then binds any raw HTTP path
// wildcards and query parameters over it.
func decodeRequest(
	loaded *schema.Schema,
	input protoreflect.MessageDescriptor,
	selected protocol,
	request Request,
	wildcards map[string]string,
	maxBodyBytes int,
) (any, error) {
	message, err := decodePayload(loaded, input, selected, request.Header, request.Body, maxBodyBytes)
	if err != nil {
		return nil, err
	}
	if selected == protocolHTTPJSON {
		if err := bindHTTPParameters(message, request, wildcards); err != nil {
			return nil, err
		}
	}
	return payloadValue(loaded, message, maxBodyBytes)
}

// decodePayload decodes one request or response body as descriptor. Every protocol
// rejects unknown fields, so a decoded message never drops part of its payload.
func decodePayload(
	loaded *schema.Schema,
	descriptor protoreflect.MessageDescriptor,
	selected protocol,
	header http.Header,
	body []byte,
	maxBodyBytes int,
) (*dynamicpb.Message, error) {
	message := dynamicpb.NewMessage(descriptor)
	switch selected {
	case protocolConnectJSON:
		if err := unmarshalProtoJSON(loaded, body, message); err != nil {
			return nil, err
		}
	case protocolHTTPJSON:
		// GET requests and 204 responses have no body to decode.
		if len(body) == 0 {
			return message, nil
		}
		if !hasJSONMediaType(header) {
			return nil, errors.Errorf("body is not JSON: %q", header.Get("Content-Type"))
		}
		if err := unmarshalProtoJSON(loaded, body, message); err != nil {
			return nil, err
		}
	case protocolGRPC:
		payload, err := grpcPayload(header, body, maxBodyBytes)
		if err != nil {
			return nil, errors.Wrap(err, "read gRPC frame")
		}
		if err := (proto.UnmarshalOptions{Resolver: loaded.Types()}).Unmarshal(payload, message); err != nil {
			return nil, errors.Wrap(err, "decode protobuf")
		}
		// ProtoJSON would silently discard unknown wire data and could hide divergence.
		if containsUnknown(message, loaded.Types()) {
			return nil, errors.New("protobuf payload contains unknown fields")
		}
	default:
		return nil, errors.Errorf("unknown protocol: %d", selected)
	}
	return message, nil
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
func grpcPayload(header http.Header, body []byte, maxBodyBytes int) ([]byte, error) {
	if len(body) < 5 {
		return nil, errors.New("missing gRPC message frame")
	}
	length := int(binary.BigEndian.Uint32(body[1:5]))
	if length != len(body)-5 {
		return nil, errors.New("gRPC body must contain exactly one message")
	}
	payload := body[5:]
	switch body[0] {
	case 0:
		return payload, nil
	case 1:
		if header.Get("Grpc-Encoding") != "gzip" {
			return nil, errors.New("unsupported gRPC compression")
		}
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, errors.Wrap(err, "open gzip body")
		}
		decompressed, err := io.ReadAll(io.LimitReader(reader, int64(maxBodyBytes)+1))
		closeErr := reader.Close()
		if err != nil {
			return nil, errors.Wrap(err, "decompress gRPC body")
		}
		if closeErr != nil {
			return nil, errors.Wrap(closeErr, "close gzip body")
		}
		if len(decompressed) > maxBodyBytes {
			return nil, errors.New("decompressed gRPC body exceeds the size limit")
		}
		return decompressed, nil
	default:
		return nil, errors.New("invalid gRPC compression flag")
	}
}

func unmarshalProtoJSON(loaded *schema.Schema, data []byte, message proto.Message) error {
	err := (protojson.UnmarshalOptions{Resolver: loaded.Types()}).Unmarshal(data, message)
	return errors.Wrap(err, "decode ProtoJSON")
}

// payloadValue converts a decoded message to the ProtoJSON value normalisers receive.
func payloadValue(loaded *schema.Schema, message proto.Message, maxBodyBytes int) (any, error) {
	data, err := (protojson.MarshalOptions{Resolver: loaded.Types()}).Marshal(message)
	if err != nil {
		return nil, errors.Wrap(err, "encode ProtoJSON")
	}
	if len(data) > maxBodyBytes {
		return nil, errors.New("decoded payload exceeds the comparison size limit")
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, errors.Wrap(err, "decode ProtoJSON value")
	}
	return payload, nil
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

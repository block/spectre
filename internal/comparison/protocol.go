package comparison

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/httpcodec"
	"github.com/block/spectre/internal/schema"
)

// protocol identifies the wire decoder used to produce one canonical JSON model.
type protocol int

const (
	protocolConnectJSON protocol = iota + 1
	protocolGRPC
	// protocolHTTPJSON is a raw HTTP response whose JSON body is the endpoint method's output.
	protocolHTTPJSON
	// protocolProtobuf is a bare serialized protobuf message carried as the HTTP body.
	protocolProtobuf
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
	case "application/x-protobuf", "application/protobuf":
		return protocolProtobuf, newEmptyResult()
	default:
		return 0, Resultf(Skipped, "request protocol is not supported: %q", mediaType)
	}
}

// conventionalMethod follows the gRPC and Connect convention of naming the
// method in the last two path segments.
func conventionalMethod(loaded *schema.Schema,
	codec *httpcodec.Codec, path string) (schema.Operation, Result) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return schema.Operation{}, Resultf(Unable, "RPC path does not identify a method: %q", path)
	}
	name := parts[len(parts)-2] + "." + parts[len(parts)-1]
	if codec.Streaming(name) {
		return schema.Operation{}, Resultf(Skipped, "streaming RPC comparison is not supported: %s", name)
	}
	operation, err := loaded.Operation(name)
	if err != nil {
		return schema.Operation{}, Resultf(Unable, "RPC method is absent from the comparison schema: %v", err)
	}
	return operation, newEmptyResult()
}

// normaliseResponses decodes both responses to the JSON values that normalisers receive.
func normaliseResponses(
	loaded *schema.Schema,
	codec *httpcodec.Codec,
	root *schema.Type,
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
		return normaliseConnect(loaded, codec, root, reference, candidate, maxResponseBytes)
	case protocolGRPC:
		return normaliseGRPC(loaded, codec, root, reference, candidate, maxResponseBytes)
	case protocolHTTPJSON:
		return normaliseHTTPJSON(loaded, codec, root, reference, candidate, maxResponseBytes)
	case protocolProtobuf:
		return normaliseProtobuf(loaded, codec, root, reference, candidate, maxResponseBytes)
	default:
		return nil, nil, Resultf(Unable, "unknown comparison protocol: %d", selected)
	}
}

func normaliseConnect(
	loaded *schema.Schema,
	codec *httpcodec.Codec,
	root *schema.Type,
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
	return decodeResponses(loaded, codec, root, protocolConnectJSON, reference, candidate, maxResponseBytes)
}

// normaliseHTTPJSON decodes every non-empty body as the method output, so error
// bodies must be modelled in the same message as successful ones.
func normaliseHTTPJSON(
	loaded *schema.Schema,
	codec *httpcodec.Codec,
	root *schema.Type,
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
	return decodeResponses(loaded, codec, root, protocolHTTPJSON, reference, candidate, maxResponseBytes)
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
	codec *httpcodec.Codec,
	root *schema.Type,
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
	return decodeResponses(loaded, codec, root, protocolGRPC, reference, candidate, maxResponseBytes)
}

// normaliseProtobuf decodes bare protobuf response bodies.
func normaliseProtobuf(
	loaded *schema.Schema,
	codec *httpcodec.Codec,
	root *schema.Type,
	reference Response,
	candidate Response,
	maxResponseBytes int,
) (referencePayload, candidatePayload any, result Result) {
	// A non-OK status carries an error body that is not the method output; the
	// caller already matched the statuses, so treat them as equivalent.
	if reference.StatusCode != http.StatusOK {
		return nil, nil, Resultf(Equivalent, "")
	}
	if !hasProtobufMediaType(reference.Header) || !hasProtobufMediaType(candidate.Header) {
		return nil, nil, Resultf(
			Unable,
			"protobuf response has an invalid content type: reference=%q candidate=%q",
			reference.Header.Get("Content-Type"),
			candidate.Header.Get("Content-Type"),
		)
	}
	return decodeResponses(loaded, codec, root, protocolProtobuf, reference, candidate, maxResponseBytes)
}

// decodeResponses decodes both response bodies as root, reporting which side failed.
func decodeResponses(
	loaded *schema.Schema,
	codec *httpcodec.Codec,
	root *schema.Type,
	selected protocol,
	reference Response,
	candidate Response,
	maxResponseBytes int,
) (referencePayload, candidatePayload any, result Result) {
	referencePayload, err := decodeResponse(loaded, codec, root, selected, reference, maxResponseBytes)
	if err != nil {
		return nil, nil, Resultf(Unable, "cannot decode reference response as %s: %v", root.Name, err)
	}
	candidatePayload, err = decodeResponse(loaded, codec, root, selected, candidate, maxResponseBytes)
	if err != nil {
		return nil, nil, Resultf(Unable, "cannot decode candidate response as %s: %v", root.Name, err)
	}
	return referencePayload, candidatePayload, newEmptyResult()
}

// decodeResponse decodes the body as the declared response type.
func decodeResponse(loaded *schema.Schema, codec *httpcodec.Codec, output *schema.Type,
	selected protocol, response Response, maxBodyBytes int,
) (any, error) {
	payload, err := decodePayload(codec, output, selected, response.Header, response.Body, maxBodyBytes)
	if err != nil {
		return nil, err
	}
	if err := loaded.Validate(output.Name, payload); err != nil {
		return nil, errors.Wrap(err, "validate response")
	}
	return payload, nil
}

// decodeRequest binds raw HTTP path and query values before validating required fields.
func decodeRequest(loaded *schema.Schema, codec *httpcodec.Codec, input *schema.Type,
	selected protocol, request Request, wildcards map[string]string, maxBodyBytes int,
) (any, error) {
	payload, err := decodePayload(codec, input, selected, request.Header, request.Body, maxBodyBytes)
	if err != nil {
		return nil, err
	}
	if selected == protocolHTTPJSON {
		object, ok := payload.(map[string]any)
		if !ok {
			return nil, errors.New("HTTP request body must be an object")
		}
		if err := bindHTTPParameters(loaded, input, object, request, wildcards); err != nil {
			return nil, err
		}
	}
	if err := loaded.Validate(input.Name, payload); err != nil {
		return nil, errors.Wrap(err, "validate request")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.Wrap(err, "encode bound request")
	}
	if len(encoded) > maxBodyBytes {
		return nil, errors.New("decoded payload exceeds the comparison size limit")
	}
	return payload, nil
}

func decodePayload(codec *httpcodec.Codec, root *schema.Type, selected protocol,
	header http.Header, body []byte, maxBodyBytes int,
) (any, error) {
	if selected == protocolHTTPJSON {
		if len(body) == 0 {
			return map[string]any{}, nil
		}
		if !hasJSONMediaType(header) {
			return nil, errors.Errorf("body is not JSON: %q", header.Get("Content-Type"))
		}
		var payload any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, errors.Wrap(err, "decode JSON")
		}
		return payload, nil
	}
	var format httpcodec.Format
	switch selected {
	case protocolConnectJSON:
		format = httpcodec.ConnectJSON
	case protocolGRPC:
		format = httpcodec.GRPC
	case protocolProtobuf:
		format = httpcodec.Protobuf
	default:
		return nil, errors.Errorf("unknown protocol: %d", selected)
	}
	return codec.Decode(root.Name, format, header, body, maxBodyBytes)
}

func hasJSONMediaType(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && mediaType == jsonMediaType
}

func hasGRPCMediaType(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && (mediaType == "application/grpc" || mediaType == "application/grpc+proto")
}

func hasProtobufMediaType(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && (mediaType == "application/x-protobuf" || mediaType == "application/protobuf")
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

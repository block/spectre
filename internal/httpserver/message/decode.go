package message

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/schema"
)

// wire identifies the decoder that produces one canonical JSON model.
type wire int

const (
	wireConnectJSON wire = iota + 1
	wireGRPC
	// wireHTTPJSON is a raw HTTP body whose JSON is the endpoint's declared type.
	wireHTTPJSON
	// wireProtobuf is a bare serialized protobuf message carried as the HTTP body.
	wireProtobuf
)

const jsonMediaType = "application/json"

// requestWire selects the RPC wire protocol from the request content type.
func requestWire(contentType string) (wire, error) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return 0, errors.Errorf("request content type is not supported: %v", err)
	}
	switch mediaType {
	case jsonMediaType:
		return wireConnectJSON, nil
	case "application/grpc", "application/grpc+proto":
		return wireGRPC, nil
	case "application/x-protobuf", "application/protobuf":
		return wireProtobuf, nil
	default:
		return 0, errors.Errorf("request protocol is not supported: %q", mediaType)
	}
}

// Response is one captured response.
type Response struct {
	// StatusCode is the backend's HTTP status before RPC-level status handling.
	StatusCode int
	Header     http.Header
	Trailer    http.Header
	// Body contains at most the configured capture limit.
	Body []byte
}

// ResponsePlan decodes the responses to one ingress request. A plan that failed
// to resolve reports the same error for every response, but still compares HTTP status.
type ResponsePlan struct {
	decoder       *Decoder
	root          *schema.Type
	selected      wire
	requestMethod string
	failure       Option[error]
	ignored       Option[string]
}

func newResponsePlan(decoder *Decoder, root *schema.Type, selected wire, requestMethod string) ResponsePlan {
	return ResponsePlan{decoder: decoder, root: root, selected: selected, requestMethod: requestMethod}
}

func failedPlan(err error) ResponsePlan {
	return ResponsePlan{failure: Some(err)}
}

func ignoredPlan(reason string) ResponsePlan {
	return ResponsePlan{failure: Some(errors.New("response is not compared")), ignored: Some(reason)}
}

// Ignored returns why the responses are never compared, if they are not.
func (p ResponsePlan) Ignored() Option[string] {
	return p.ignored
}

// Decode returns a response's comparable content. A response with no message
// compares by status alone.
func (p ResponsePlan) Decode(response Response) protocol.Content {
	if p.selected == wireGRPC {
		return p.decodeGRPC(response)
	}
	status := Some(strconv.Itoa(response.StatusCode))
	if failure, failed := p.failure.Get(); failed {
		return protocol.Content{Status: status, Failure: Some(failure)}
	}
	var message Option[protocol.Message]
	var err error
	switch p.selected {
	case wireConnectJSON:
		message, err = p.decodeConnect(response)
	case wireHTTPJSON:
		message, err = p.decodeHTTPJSON(response)
	case wireProtobuf:
		message, err = p.decodeProtobuf(response)
	case wireGRPC:
		err = errors.New("gRPC responses must be decoded with their RPC status")
	default:
		err = errors.Errorf("unknown wire protocol: %d", p.selected)
	}
	return newContent(status, message, err)
}

func newContent(status Option[string], message Option[protocol.Message], err error) protocol.Content {
	if err != nil {
		return protocol.Content{Status: status, Failure: Some(err)}
	}
	return protocol.Content{Status: status, Message: message}
}

func (p ResponsePlan) typed(payload any) Option[protocol.Message] {
	return Some(protocol.Message{Type: Some(p.root.Name), Value: payload})
}

// decodeConnect compares error envelopes untyped, because they are not instances
// of the RPC output message.
func (p ResponsePlan) decodeConnect(response Response) (Option[protocol.Message], error) {
	if !hasJSONMediaType(response.Header) {
		return None[protocol.Message](), errors.Errorf("Connect response is not JSON: %q", response.Header.Get("Content-Type"))
	}
	if response.StatusCode != http.StatusOK {
		payload, err := connectErrorPayload(response.Body)
		if err != nil {
			return None[protocol.Message](), errors.Wrap(err, "Connect error response contains malformed JSON")
		}
		return Some(protocol.Message{Type: None[schema.TypeRef](), Value: payload}), nil
	}
	return p.decodeTyped(response)
}

// decodeHTTPJSON validates bodies as the declared type, except for empty HEAD,
// 204, and 304 responses. Error bodies use the same type as successful ones.
func (p ResponsePlan) decodeHTTPJSON(response Response) (Option[protocol.Message], error) {
	bodyless := p.requestMethod == http.MethodHead || response.StatusCode == http.StatusNoContent ||
		response.StatusCode == http.StatusNotModified
	if bodyless && len(response.Body) == 0 {
		return None[protocol.Message](), nil
	}
	return p.decodeTyped(response)
}

// decodeGRPC compares the gRPC status, and the message only when it is OK. A
// non-200 HTTP status is the comparable status, because no gRPC status applies.
func (p ResponsePlan) decodeGRPC(response Response) protocol.Content {
	if response.StatusCode != http.StatusOK {
		status := Some(strconv.Itoa(response.StatusCode))
		return newContent(status, None[protocol.Message](), errors.Errorf("gRPC response has HTTP status %d", response.StatusCode))
	}
	unknown := None[string]()
	if !hasGRPCMediaType(response.Header) {
		return newContent(unknown, None[protocol.Message](), errors.Errorf("gRPC response has an invalid content type: %q", response.Header.Get("Content-Type")))
	}
	code, err := grpcStatus(response)
	if err != nil {
		return newContent(unknown, None[protocol.Message](), errors.Wrap(err, "response has no valid gRPC status"))
	}
	status := Some("grpc-status " + strconv.Itoa(code))
	if code != 0 {
		return newContent(status, None[protocol.Message](), nil)
	}
	message, err := p.decodeTyped(response)
	return newContent(status, message, err)
}

// decodeProtobuf decodes bare protobuf bodies. A non-OK status carries an error
// body that is not the method output, so only the status is compared.
func (p ResponsePlan) decodeProtobuf(response Response) (Option[protocol.Message], error) {
	if response.StatusCode != http.StatusOK {
		return None[protocol.Message](), nil
	}
	if !hasProtobufMediaType(response.Header) {
		return None[protocol.Message](), errors.Errorf("protobuf response has an invalid content type: %q", response.Header.Get("Content-Type"))
	}
	return p.decodeTyped(response)
}

func (p ResponsePlan) decodeTyped(response Response) (Option[protocol.Message], error) {
	payload, err := p.decoder.decodeResponse(p.root, p.selected, response)
	if err != nil {
		return None[protocol.Message](), errors.Wrapf(err, "decode response as %s", p.root.Name)
	}
	return p.typed(payload), nil
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

// decompressHTTPBody returns the body decoded per its Content-Encoding: identity
// passes through, gzip is inflated under the size limit, others are rejected.
func decompressHTTPBody(header http.Header, body []byte, maxBodyBytes int) ([]byte, error) {
	switch encoding := strings.ToLower(strings.TrimSpace(header.Get("Content-Encoding"))); encoding {
	case "", "identity":
		return body, nil
	case "gzip", "x-gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, errors.Wrap(err, "open gzip body")
		}
		decompressed, err := io.ReadAll(io.LimitReader(reader, int64(maxBodyBytes)+1))
		closeErr := reader.Close()
		if err != nil {
			return nil, errors.Wrap(err, "decompress gzip body")
		}
		if closeErr != nil {
			return nil, errors.Wrap(closeErr, "close gzip body")
		}
		if len(decompressed) > maxBodyBytes {
			return nil, errors.New("decompressed body exceeds the size limit")
		}
		return decompressed, nil
	default:
		return nil, errors.Errorf("unsupported Content-Encoding: %q", encoding)
	}
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

// grpcStatus reads the status from the headers of a trailers-only response, or
// from the trailers.
func grpcStatus(response Response) (int, error) {
	value := response.Header.Get("Grpc-Status")
	if value == "" {
		value = response.Trailer.Get("Grpc-Status")
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

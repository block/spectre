// Package message decodes HTTP requests and responses into engine messages.
package message

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/httpserver/codec"
	"github.com/block/spectre/internal/httpserver/route"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/schema"
)

// Decoder decodes HTTP payloads as the schema types that scripts declare, or that
// RPC paths name. It is immutable and safe for concurrent use.
type Decoder struct {
	schema       *schema.Schema
	codec        *codec.Codec
	routes       *route.Matcher
	selector     RuleSelector
	maxBodyBytes int
}

// RuleSelector picks the first of an inbound request's rules that selects a response.
type RuleSelector interface {
	SelectRule(ctx context.Context, rules []javascript.Rule, request javascript.Request, response javascript.Response) (Option[javascript.Rule], error)
}

// New checks the descriptors against the schema, and every declared request
// wildcard against its type, before decoding anything.
func New(loaded *schema.Schema, set *descriptorpb.FileDescriptorSet, routes *route.Matcher, selector RuleSelector, maxBodyBytes int) (*Decoder, error) {
	if selector == nil {
		return nil, errors.New("rule selector is required")
	}
	wire, err := codec.New(loaded, set)
	if err != nil {
		return nil, errors.Wrap(err, "configure wire decoding")
	}
	for _, declared := range routes.Requests() {
		root, err := loaded.Type(declared.Type)
		if err != nil {
			return nil, errors.Wrapf(err, "resolve endpoint %q", declared.Pattern)
		}
		for _, name := range route.Wildcards(declared.Pattern) {
			if _, err := resolveBinding(root, name, false); err != nil {
				return nil, errors.Wrapf(err, "bind endpoint %q wildcard %q", declared.Pattern, name)
			}
		}
	}
	return &Decoder{schema: loaded, codec: wire, routes: routes, selector: selector, maxBodyBytes: maxBodyBytes}, nil
}

// Request is one captured dependency request.
type Request struct {
	// Method is the HTTP request method.
	Method string
	// Host selects the destination's endpoints. Any port is ignored.
	Host string
	// Path is escaped, as endpoints match it the way ServeMux does.
	Path string
	// RawQuery is the encoded query string without the leading "?".
	RawQuery string
	// Header holds the request headers.
	Header http.Header
	// Body contains at most the configured capture limit.
	Body []byte
	// Overflow reports that Body is truncated and must not be decoded.
	Overflow bool
}

// Request decodes a dependency request as its declared type, or as its RPC
// method's input. An error means the request cannot be identified.
func (d *Decoder) Request(request Request) (schema.Value, error) {
	if request.Overflow {
		return nil, errors.New("request exceeds the comparison size limit")
	}
	endpoint, wildcards, declared := d.routes.Request(request.Method, request.Host, request.Path)
	selected, err := requestWire(request.Header.Get("Content-Type"))
	if declared && selected != wireGRPC && selected != wireProtobuf {
		selected, err = wireHTTPJSON, nil
	}
	if err != nil {
		return nil, err
	}
	name := endpoint.Type
	if !declared {
		operation, err := d.conventionalMethod(request.Path)
		if err != nil {
			return nil, err
		}
		name = operation.Request
	}
	root, err := d.schema.Type(name)
	if err != nil {
		return nil, errors.Wrap(err, "endpoint type is unusable")
	}
	instance, err := d.decodeRequest(root, selected, request, wildcards)
	if err != nil {
		return nil, errors.Wrapf(err, "decode request as %s", root.Name)
	}
	return instance, nil
}

// SourceResponse decodes a successful response to a dependency request with method and header as name,
// for replacements to read. Any other response is a failure.
func (d *Decoder) SourceResponse(method string, header http.Header, name schema.TypeRef, response Response) protocol.Content {
	status := Some(strconv.Itoa(response.StatusCode))
	if response.StatusCode != http.StatusOK {
		return protocol.Content{Status: status, Failure: Some(errors.Errorf("response has status %d", response.StatusCode))}
	}
	requested, err := requestWire(header.Get("Content-Type"))
	if err != nil {
		requested = wireHTTPJSON
	}
	declared, err := d.schema.Type(name)
	if err != nil {
		return protocol.Content{Status: status, Failure: Some(errors.Wrap(err, "endpoint type is unusable"))}
	}
	content := d.matched(javascript.ActionMatch{Type: declared}, requested, method).decode(response)
	if _, decoded := content.Message.Get(); decoded || content.Failure.Ok() {
		return content
	}
	code, _ := content.Status.Get()
	return protocol.Content{Status: content.Status, Failure: Some(errors.Errorf("response has status %q and no message", code))}
}

// Answer is a successful response that the server encodes itself.
type Answer struct {
	Header http.Header
	Body   []byte
}

// Answer encodes message as the successful response to a dependency request with
// header, in its wire format: bare protobuf, or HTTP JSON otherwise.
func (d *Decoder) Answer(header http.Header, message schema.Value) (Answer, error) {
	requested, err := requestWire(header.Get("Content-Type"))
	switch {
	case err == nil && requested == wireGRPC:
		return Answer{}, errors.New("gRPC requests cannot be answered")
	case err == nil && requested == wireProtobuf:
		body, err := d.codec.Encode(message)
		if err != nil {
			return Answer{}, errors.Wrapf(err, "encode answer as %s", message.Type())
		}
		return Answer{Header: http.Header{"Content-Type": {header.Get("Content-Type")}}, Body: body}, nil
	}
	body, err := json.Marshal(message)
	if err != nil {
		return Answer{}, errors.Wrapf(err, "encode answer as %s", message.Type())
	}
	return Answer{Header: http.Header{"Content-Type": {jsonMediaType}}, Body: body}, nil
}

// Responses decides how ingress compares the responses to one request.
func (d *Decoder) Responses(method, path, rawQuery string, header http.Header) ResponsePlan {
	request := javascript.Request{Method: method, Path: path, Header: header.Clone()}
	if excludedRequestPath(path) {
		return ignoredResponsePlan(d, request, "gRPC namespace is excluded from response comparison")
	}
	rules, ignored := d.routes.Responses(method, path, header)
	if reason, ok := ignored.Get(); ok {
		return ignoredResponsePlan(d, request, reason)
	}
	// Rules would compare a stream as one message, so they cannot make it comparable.
	if operation, streaming := d.streamingMethod(path); streaming {
		return unsupportedResponsePlan(d, request, "streaming RPC comparison is not supported: "+operation)
	}
	requested, err := requestWire(header.Get("Content-Type"))
	if err != nil {
		requested = wireHTTPJSON
	}
	return newResponsePlan(d, request, rules, requested, d.unruled(method, path, rawQuery, header))
}

// selectRule returns the first of rules that selects a response.
func (d *Decoder) selectRule(ctx context.Context, rules []javascript.Rule, request javascript.Request, response javascript.Response) (Option[javascript.Rule], error) {
	return errors.WithStack2(d.selector.SelectRule(ctx, rules, request, response))
}

func (d *Decoder) decompress(header http.Header, body []byte) ([]byte, error) {
	return decompressHTTPBody(header, body, d.maxBodyBytes)
}

// unruled decodes responses no rule selects as their RPC method's output. An
// unrecognised content type is a gap in the scripts, so it fails rather than skips.
func (d *Decoder) unruled(method, path, rawQuery string, header http.Header) decoding {
	selected, err := requestWire(header.Get("Content-Type"))
	switch {
	case err != nil && header.Get("Content-Type") == "" && connectGet(method, rawQuery):
		selected = wireConnectJSON
	case err != nil:
		return failedDecoding(errors.Wrap(err, "undeclared request"))
	}
	operation, err := d.conventionalMethod(path)
	if err != nil {
		return failedDecoding(err)
	}
	output, err := d.schema.Type(operation.Response)
	if err != nil {
		return failedDecoding(errors.Wrap(err, "endpoint type is unusable"))
	}
	return newDecoding(d, output, selected, method)
}

// matched decodes the responses a rule selects as its type. Their bodies are HTTP
// JSON unless the request is gRPC or protobuf, whose messages need an object type.
func (d *Decoder) matched(action javascript.ActionMatch, requested wire, method string) decoding {
	selected := requested
	if selected != wireGRPC && selected != wireProtobuf {
		selected = wireHTTPJSON
	}
	switch action.Type.(type) {
	case schema.StringType, *schema.ObjectType:
	case schema.UnknownType:
		if selected != wireHTTPJSON {
			return failedDecoding(errors.New("protobuf responses cannot be decoded as unknown"))
		}
	case schema.NumberType, schema.BooleanType, schema.EnumType, schema.NullableType, schema.ListType, schema.MapType:
		return failedDecoding(errors.Errorf("responses cannot be decoded as %s", action.Type))
	}
	return newDecoding(d, action.Type, selected, method)
}

// connectError returns the type of a Connect error body, which may be empty.
func (d *Decoder) connectError() (schema.Type, error) {
	declared, err := d.schema.Type(schema.ConnectError)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return schema.NullableType{Type: declared}, nil
}

func connectGet(method, rawQuery string) bool {
	query, err := url.ParseQuery(rawQuery)
	return err == nil && method == http.MethodGet && query.Get("encoding") == "json"
}

func excludedRequestPath(path string) bool {
	// Reflection and other gRPC control services are not application schema targets.
	return strings.HasPrefix(strings.Trim(path, "/"), "grpc.")
}

// rpcMethod follows the gRPC and Connect convention of naming the method in
// the last two path segments.
func rpcMethod(path string) (name string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return "", false
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1], true
}

func (d *Decoder) streamingMethod(path string) (name string, streaming bool) {
	name, ok := rpcMethod(path)
	return name, ok && d.codec.Streaming(name)
}

func (d *Decoder) conventionalMethod(path string) (schema.Operation, error) {
	name, ok := rpcMethod(path)
	if !ok {
		return schema.Operation{}, errors.Errorf("RPC path does not identify a method: %q", path)
	}
	if d.codec.Streaming(name) {
		return schema.Operation{}, errors.Errorf("streaming RPC comparison is not supported: %s", name)
	}
	operation, err := d.schema.Operation(name)
	if err != nil {
		return schema.Operation{}, errors.Wrap(err, "RPC method is absent from the comparison schema")
	}
	return operation, nil
}

// decodeResponse decodes a response body as declared.
func (d *Decoder) decodeResponse(declared schema.Type, selected wire, response Response) (schema.Value, error) {
	payload, err := d.decodePayload(declared, selected, response.Header, response.Body)
	if err != nil {
		return nil, err
	}
	instance, err := schema.As(declared, payload)
	if err != nil {
		return nil, errors.Wrap(err, "validate response")
	}
	return instance, nil
}

// decodeRequest binds raw HTTP path and query values before validating required fields.
func (d *Decoder) decodeRequest(input *schema.ObjectType, selected wire, request Request, wildcards map[string]string) (schema.Value, error) {
	payload, err := d.decodePayload(input, selected, request.Header, request.Body)
	if err != nil {
		return nil, err
	}
	if selected == wireHTTPJSON {
		object, ok := payload.(schema.ObjectValue)
		if !ok {
			return nil, errors.New("HTTP request body must be an object")
		}
		payload, err = bindHTTPParameters(input, object.Members(), request, wildcards)
		if err != nil {
			return nil, err
		}
	}
	instance, err := schema.As(input, payload)
	if err != nil {
		return nil, errors.Wrap(err, "validate request")
	}
	encoded, err := json.Marshal(instance)
	if err != nil {
		return nil, errors.Wrap(err, "encode bound request")
	}
	if len(encoded) > d.maxBodyBytes {
		return nil, errors.New("decoded payload exceeds the comparison size limit")
	}
	return instance, nil
}

// decodePayload decodes a body as JSON of type unknown, for validation as declared. Only
// object types decode from protobuf wires, since their descriptors share the type's name.
func (d *Decoder) decodePayload(declared schema.Type, selected wire, header http.Header, body []byte) (schema.Value, error) {
	if selected == wireHTTPJSON {
		if len(body) == 0 {
			return errors.WithStack2(schema.NewObject(nil))
		}
		if !hasJSONMediaType(header) {
			return nil, errors.Errorf("body is not JSON: %q", header.Get("Content-Type"))
		}
		decompressed, err := decompressHTTPBody(header, body, d.maxBodyBytes)
		if err != nil {
			return nil, err
		}
		return decodeJSON(decompressed)
	}
	message, ok := schema.NonNull(declared).(*schema.ObjectType)
	if !ok {
		return nil, errors.Errorf("protobuf payloads cannot be decoded as %s", declared)
	}
	var format codec.Format
	switch selected {
	case wireConnectJSON:
		format = codec.ConnectJSON
	case wireGRPC:
		format = codec.GRPC
	case wireProtobuf:
		format = codec.Protobuf
	case wireHTTPJSON:
		return nil, errors.New("HTTP JSON must be decoded without a wire codec")
	default:
		return nil, errors.Errorf("unknown wire protocol: %d", selected)
	}
	data, err := d.codec.Decode(message.Name, format, header, body, d.maxBodyBytes)
	if err != nil {
		return nil, errors.Wrap(err, "decode wire payload")
	}
	return decodeJSON(data)
}

func decodeJSON(data []byte) (schema.Value, error) {
	payload, err := schema.Decode(schema.UnknownType{}, data)
	return payload, errors.Wrap(err, "decode JSON")
}

// Package message decodes HTTP requests and responses into engine messages.
package message

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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
			if _, err := resolveBinding(loaded, root, name, false); err != nil {
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
func (d *Decoder) Request(request Request) (protocol.Message, error) {
	if request.Overflow {
		return protocol.Message{}, errors.New("request exceeds the comparison size limit")
	}
	endpoint, wildcards, declared := d.routes.Request(request.Method, request.Host, request.Path)
	selected, err := requestWire(request.Header.Get("Content-Type"))
	if declared && selected != wireGRPC && selected != wireProtobuf {
		selected, err = wireHTTPJSON, nil
	}
	if err != nil {
		return protocol.Message{}, err
	}
	name := endpoint.Type
	if !declared {
		operation, err := d.conventionalMethod(request.Path)
		if err != nil {
			return protocol.Message{}, err
		}
		name = operation.Request
	}
	root, err := d.schema.Type(name)
	if err != nil {
		return protocol.Message{}, errors.Wrap(err, "endpoint type is unusable")
	}
	payload, err := d.decodeRequest(root, selected, request, wildcards)
	if err != nil {
		return protocol.Message{}, errors.Wrapf(err, "decode request as %s", root.Name)
	}
	return protocol.Message{Type: Some(root.Name), Value: payload}, nil
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

// unruled decodes responses no rule selects as their RPC method's output.
func (d *Decoder) unruled(method, path, rawQuery string, header http.Header) decoding {
	selected, err := requestWire(header.Get("Content-Type"))
	switch {
	case err != nil && header.Get("Content-Type") == "" && connectGet(method, rawQuery):
		selected = wireConnectJSON
	case err != nil:
		return ignoredDecoding(err.Error())
	}
	if operation, streaming := d.streamingMethod(path); streaming {
		return ignoredDecoding("streaming RPC comparison is not supported: " + operation)
	}
	operation, err := d.conventionalMethod(path)
	if err != nil {
		return failedDecoding(err)
	}
	root, err := d.schema.Type(operation.Response)
	if err != nil {
		return failedDecoding(errors.Wrap(err, "endpoint type is unusable"))
	}
	return newDecoding(d, root, selected, method)
}

// matched decodes the responses a rule selects as its type, or as text. Their
// bodies are HTTP JSON unless the request is gRPC or protobuf.
func (d *Decoder) matched(action javascript.ActionMatch, requested wire, method string) decoding {
	name, typed := action.Type.Get()
	if !typed {
		return textDecoding(d, method)
	}
	selected := requested
	if selected != wireGRPC && selected != wireProtobuf {
		selected = wireHTTPJSON
	}
	root, err := d.schema.Type(name)
	if err != nil {
		return failedDecoding(errors.Wrap(err, "endpoint type is unusable"))
	}
	return newDecoding(d, root, selected, method)
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

// decodeResponse decodes a response body and validates it as root.
func (d *Decoder) decodeResponse(root *schema.Type, selected wire, response Response) (any, error) {
	payload, err := d.decodePayload(root, selected, response.Header, response.Body)
	if err != nil {
		return nil, err
	}
	if err := d.schema.Validate(root.Name, payload); err != nil {
		return nil, errors.Wrap(err, "validate response")
	}
	return payload, nil
}

// decodeRequest binds raw HTTP path and query values before validating required fields.
func (d *Decoder) decodeRequest(input *schema.Type, selected wire, request Request, wildcards map[string]string) (any, error) {
	payload, err := d.decodePayload(input, selected, request.Header, request.Body)
	if err != nil {
		return nil, err
	}
	if selected == wireHTTPJSON {
		object, ok := payload.(map[string]any)
		if !ok {
			return nil, errors.New("HTTP request body must be an object")
		}
		if err := bindHTTPParameters(d.schema, input, object, request, wildcards); err != nil {
			return nil, err
		}
	}
	if err := d.schema.Validate(input.Name, payload); err != nil {
		return nil, errors.Wrap(err, "validate request")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.Wrap(err, "encode bound request")
	}
	if len(encoded) > d.maxBodyBytes {
		return nil, errors.New("decoded payload exceeds the comparison size limit")
	}
	return payload, nil
}

func (d *Decoder) decodePayload(root *schema.Type, selected wire, header http.Header, body []byte) (any, error) {
	if selected == wireHTTPJSON {
		if len(body) == 0 {
			return map[string]any{}, nil
		}
		if !hasJSONMediaType(header) {
			return nil, errors.Errorf("body is not JSON: %q", header.Get("Content-Type"))
		}
		decompressed, err := decompressHTTPBody(header, body, d.maxBodyBytes)
		if err != nil {
			return nil, err
		}
		var payload any
		if err = json.Unmarshal(decompressed, &payload); err != nil {
			return nil, errors.Wrap(jsonError(err), "decode JSON")
		}
		return payload, nil
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
	payload, err := d.codec.Decode(root.Name, format, header, body, d.maxBodyBytes)
	return payload, errors.Wrap(err, "decode wire payload")
}

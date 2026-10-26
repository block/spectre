package message_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/httpserver/message"
	"github.com/block/spectre/internal/httpserver/route"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/quarantine"
	"github.com/block/spectre/internal/schema"
)

// maxBodyBytes is the default capture limit.
const maxBodyBytes = 1 << 20

// weatherScripts declares the endpoints that most decoder tests resolve against.
const weatherScripts = `
	spectre.ingress.match<v1.Response>("http", "GET /v1/forecast");
	spectre.ingress.ignore("http", "GET /_status");
	// Ingress endpoints never type dependency requests.
	spectre.ingress.match<v1.Response>("http", "POST /test.v1.Service/Rename");
	spectre.egress.match<weather.HTTPQuery>("http", "GET weather.example/v1/locations/{location}/forecast");
	spectre.egress.match<weather.HTTPQuery>("http", "POST weather.example/v1/search");
`

func TestClassifiesResponses(t *testing.T) {
	decoder := newDecoder(t, weatherScripts)
	compared := None[string]()
	for name, test := range map[string]struct {
		method      string
		path        string
		rawQuery    string
		contentType string
		ignored     Option[string]
	}{
		"RPCPath":              {method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/json", ignored: compared},
		"DeclaredHTTPEndpoint": {method: http.MethodGet, path: "/v1/forecast", ignored: compared},
		"IgnoredEndpoint":      {method: http.MethodGet, path: "/_status", ignored: Some(`endpoint "GET /_status" is ignored`)},
		"GRPCNamespace": {
			method: http.MethodPost, path: "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", contentType: "application/grpc",
			ignored: Some("gRPC namespace is excluded from response comparison"),
		},
		"Streaming": {
			method: http.MethodPost, path: "/test.v1.Service/Watch", contentType: "application/grpc",
			ignored: Some("streaming RPC comparison is not supported: test.v1.Service.Watch"),
		},
		"GRPCWeb": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/grpc-web+proto",
			ignored: Some(`request protocol is not supported: "application/grpc-web+proto"`),
		},
		"ConnectGet": {method: http.MethodGet, path: "/test.v1.Service/Get", rawQuery: "encoding=json&message=%7B%7D", ignored: compared},
		// Without a content type, only Connect GET requests are compared as Connect JSON.
		"UndeclaredGetWithoutContentType": {
			method: http.MethodGet, path: "/test.v1.Service/Get",
			ignored: Some("request content type is not supported: mime: no media type"),
		},
		// Only the declared method matches, so neither POST nor HEAD is compared as GET.
		"UndeclaredMethod": {
			method: http.MethodPost, path: "/v1/forecast",
			ignored: Some("request content type is not supported: mime: no media type"),
		},
		"HeadIsNotGet": {
			method: http.MethodHead, path: "/v1/forecast",
			ignored: Some("request content type is not supported: mime: no media type"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			header := http.Header{}
			if test.contentType != "" {
				header.Set("Content-Type", test.contentType)
			}

			plan := decoder.Responses(test.method, test.path, test.rawQuery, header)

			assert.Equal(t, test.ignored, plan.Ignored())
		})
	}
}

func TestUnresolvableResponsesFailToDecode(t *testing.T) {
	decoder := newDecoder(t, weatherScripts)
	for name, test := range map[string]struct {
		method  string
		path    string
		message string
	}{
		"UndeclaredMethod": {
			method:  http.MethodPost,
			path:    "/v1/forecast",
			message: `RPC method is absent from the comparison schema: operation "v1.forecast" is not declared`,
		},
		"EgressEndpoint": {
			method:  http.MethodPost,
			path:    "/v1/search",
			message: `operation "v1.search" is not declared`,
		},
		"MissingRPCMethod": {
			method:  http.MethodPost,
			path:    "/test.v1.Service/Missing",
			message: `operation "test.v1.Service.Missing" is not declared`,
		},
		"NoRPCMethod": {
			method:  http.MethodPost,
			path:    "/forecast",
			message: `RPC path does not identify a method: "/forecast"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			plan := decoder.Responses(test.method, test.path, "", http.Header{"Content-Type": []string{"application/json"}})
			assert.Equal(t, None[string](), plan.Ignored())

			content := plan.Decode(connectResponse(`{}`))

			// The HTTP status still compares when the plan cannot decode the body.
			failure := describe(content)
			assert.Contains(t, failure.failure, test.message)
			failure.failure = test.message
			assert.Equal(t, decoded{status: Some("200"), failure: test.message}, failure)
		})
	}
}

func TestDecodesRequests(t *testing.T) {
	decoder := newDecoder(t, weatherScripts)
	query := func(value map[string]any) protocol.Message {
		return protocol.Message{Type: Some[schema.TypeRef]("weather.HTTPQuery"), Value: value}
	}
	rpcQuery := protocol.Message{
		Type:  Some[schema.TypeRef]("test.v1.Query"),
		Value: map[string]any{"location": "london", "days": float64(3), "tags": []any{}},
	}
	for name, test := range map[string]struct {
		request  message.Request
		expected protocol.Message
	}{
		"PathAndQuery": {
			request: forecastRequest("london", "days=3&units=METRIC&tags=wind&tags=rain&filter.minDays=2"),
			expected: query(map[string]any{
				"location": "london", "days": float64(3), "units": "METRIC",
				"tags": []any{"wind", "rain"}, "filter": map[string]any{"minDays": float64(2)},
			}),
		},
		"QueryOrder": {
			request:  forecastRequest("london", "tags=wind&units=METRIC&tags=rain&days=3&filter.minDays=2"),
			expected: query(map[string]any{"location": "london", "days": float64(3), "units": "METRIC", "tags": []any{"wind", "rain"}, "filter": map[string]any{"minDays": float64(2)}}),
		},
		"Body": {
			request:  searchRequest(`{"location":"london","days":3,"tags":["wind","rain"],"filter":{"minDays":2},"units":"METRIC"}`),
			expected: query(map[string]any{"location": "london", "days": float64(3), "units": "METRIC", "tags": []any{"wind", "rain"}, "filter": map[string]any{"minDays": float64(2)}}),
		},
		"Absent":          {request: forecastRequest("london", ""), expected: query(map[string]any{"location": "london"})},
		"ExplicitDefault": {request: forecastRequest("london", "days=0"), expected: query(map[string]any{"location": "london", "days": float64(0)})},
		"EscapedWildcard": {request: forecastRequest("new%20york", ""), expected: query(map[string]any{"location": "new york"})},
		"ConnectJSON":     {request: rpcRequest("Find", `{"location":"london","days":3}`), expected: rpcQuery},
		"GRPC":            {request: grpcRequest(t, "Find", queryProto("london", 3)), expected: rpcQuery},
		"Protobuf":        {request: protobufRequest(queryProto("london", 3)), expected: rpcQuery},
		"RPCMethodSelectsType": {
			request:  rpcRequest("Get", `{}`),
			expected: protocol.Message{Type: Some[schema.TypeRef]("test.v1.Request"), Value: map[string]any{}},
		},
		"IngressEndpointIgnored": {
			request:  rpcRequest("Rename", `{"name":"alice"}`),
			expected: protocol.Message{Type: Some[schema.TypeRef]("test.v1.User"), Value: map[string]any{"name": "alice"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := decoder.Request(test.request)

			assert.NoError(t, err)
			assert.Equal(t, test.expected, decoded)
		})
	}
}

func TestRejectsUnidentifiableRequests(t *testing.T) {
	decoder := newDecoder(t, weatherScripts)
	overflow := searchRequest(`{}`)
	overflow.Overflow = true
	getWithBody := withContentType(forecastRequest("london", ""), "application/json")
	getWithBody.Body = []byte(`{}`)
	for name, test := range map[string]struct {
		request message.Request
		message string
	}{
		"UnknownQueryParameter": {request: forecastRequest("london", "unknown=1"), message: `bind query parameter "unknown": type "weather.HTTPQuery" has no field "unknown"`},
		"UnknownBodyField":      {request: searchRequest(`{"unknown":1}`), message: `has a field it does not declare`},
		"InvalidValue":          {request: forecastRequest("london", "days=many"), message: `parse field "days"`},
		"UnknownEnumValue":      {request: forecastRequest("london", "units=KELVIN"), message: `value is not a declared enum literal`},
		"RepeatedSingular":      {request: forecastRequest("london", "days=1&days=2"), message: `field "days" is not repeated`},
		"MessageParameter":      {request: forecastRequest("london", "filter=1"), message: `field "filter" is not a scalar`},
		"PathAndQuery":          {request: forecastRequest("london", "location=paris"), message: `field "location" is already bound`},
		"BodyAndQuery":          {request: withQuery(searchRequest(`{"days":3}`), "days=3"), message: `field "days" is already bound`},
		"ProtoNameRejected":     {request: forecastRequest("london", "filter.minDays=2&filter.min_days=2"), message: `has no field "min_days"`},
		"GetWithBody":           {request: getWithBody, message: "GET request has a body"},
		"BodyNotJSON":           {request: withContentType(searchRequest(`{}`), "text/plain"), message: `body is not JSON: "text/plain"`},
		"UnknownHost":           {request: withHost(forecastRequest("london", ""), "other.example"), message: "request content type is not supported"},
		"UnknownMethod":         {request: rpcRequest("Missing", `{}`), message: `operation "test.v1.Service.Missing" is not declared`},
		"Overflow":              {request: overflow, message: "request exceeds the comparison size limit"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decoder.Request(test.request)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

// Request failures become quarantine reasons, which leave the mirror.
func TestRequestFailuresOmitValues(t *testing.T) {
	decoder := newDecoder(t, weatherScripts)
	for name, test := range map[string]struct {
		request message.Request
		message string
	}{
		"Number":      {request: forecastRequest("london", "days=secret"), message: `bind query parameter "days": parse field "days": parse number: ParseFloat: invalid syntax`},
		"QueryEscape": {request: forecastRequest("london", "days=%zzsecret"), message: "query is not valid URL encoding"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decoder.Request(test.request)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
			assert.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestRejectsUnbindableEndpoints(t *testing.T) {
	for name, test := range map[string]struct {
		pattern string
		message string
	}{
		"UnknownField": {pattern: "GET weather.example/v1/{place}", message: `type "weather.HTTPQuery" has no field "place"`},
		"Repeated":     {pattern: "GET weather.example/v1/{tags}", message: `field "tags" is repeated`},
		"Message":      {pattern: "GET weather.example/v1/{filter}", message: `field "filter" is not a scalar`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildDecoder(t, descriptorSet(), testDeclarations(t),
				map[string]string{"weather.ts": module(`spectre.egress.match<weather.HTTPQuery>("http", "` + test.pattern + `");`)})
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `bind endpoint "`+test.pattern+`"`)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestBindsTextWithoutDescriptors(t *testing.T) {
	decoder := newRawDecoder(t)
	item := func(value map[string]any) protocol.Message {
		return protocol.Message{Type: Some[schema.TypeRef]("raw.Item"), Value: value}
	}
	ratio := func(value map[string]any) protocol.Message {
		return protocol.Message{Type: Some[schema.TypeRef]("raw.Ratio"), Value: value}
	}
	for name, test := range map[string]struct {
		request  message.Request
		expected protocol.Message
		message  string
	}{
		"RequiredPathScalar":  {request: apiRequest("/items/one", ""), expected: item(map[string]any{"id": "one"})},
		"ExplicitDefault":     {request: apiRequest("/items/one", "count=0"), expected: item(map[string]any{"id": "one", "count": float64(0)})},
		"NumberLiteral":       {request: apiRequest("/ratio", "ratio=NaN"), expected: ratio(map[string]any{"ratio": "NaN"})},
		"Number":              {request: apiRequest("/ratio", "ratio=1"), expected: ratio(map[string]any{"ratio": float64(1)})},
		"NonFiniteNumber":     {request: apiRequest("/ratio", "ratio=Infinity"), message: "number must be finite"},
		"MissingRequiredText": {request: apiRequest("/ratio", ""), message: `required field "ratio" of type "raw.Ratio" is missing`},
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := decoder.Request(test.request)
			if test.message != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), test.message)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.expected, decoded)
		})
	}
}

// discardQuarantine accepts quarantine requests, which decoding never makes.
type discardQuarantine struct{}

func (discardQuarantine) Quarantine(_ context.Context, _ quarantine.Reason) {}

// buildDecoder declares the scripts' endpoints through the engine, as servers do.
func buildDecoder(t *testing.T, set *descriptorpb.FileDescriptorSet, declarations, scripts map[string]string) (*message.Decoder, error) {
	t.Helper()
	config := protocol.NewConfig()
	config.Comparison.Schema.SchemaDir = writeFiles(t, declarations)
	config.Comparison.ScriptsDir = writeFiles(t, scripts)
	routes := route.NewMatcher()
	matchers := map[boundary.Protocol]protocol.Matcher{boundary.ProtocolHTTP: routes}
	engine, err := protocol.NewEngine(t.Context(), config, matchers, discardQuarantine{})
	assert.NoError(t, err)
	return message.New(engine.Schema(), set, routes, maxBodyBytes)
}

func newDecoder(t *testing.T, script string) *message.Decoder {
	t.Helper()
	decoder, err := buildDecoder(t, descriptorSet(), testDeclarations(t), map[string]string{"test.ts": module(script)})
	assert.NoError(t, err)
	return decoder
}

// rawDeclarations type plain HTTP JSON, so they have no protobuf descriptors.
const rawDeclarations = `
declare module "raw" {
  interface User { name: string; }
  interface Response {
    stable: string;
    ignored?: string;
    count?: number;
    enabled?: boolean;
    roles?: string[];
    owner?: User;
    metadata?: Record<string, string>;
  }
  interface Optional { stable?: string; }
  interface Item { id: string; count?: number; }
  interface Ratio { ratio: number | "NaN"; }
}
`

func newRawDecoder(t *testing.T) *message.Decoder {
	t.Helper()
	decoder, err := buildDecoder(t, &descriptorpb.FileDescriptorSet{},
		map[string]string{"raw.d.ts": rawDeclarations},
		map[string]string{"raw.ts": `
			import * as spectre from "spectre";
			import type * as raw from "raw";
			spectre.ingress.match<raw.Response>("http", "GET /raw");
			spectre.ingress.match<raw.Response>("http", "HEAD /raw");
			spectre.ingress.match<raw.Optional>("http", "GET /optional");
			spectre.egress.match<raw.Item>("http", "GET api.example/items/{id}");
			spectre.egress.match<raw.Ratio>("http", "GET api.example/ratio");
		`})
	assert.NoError(t, err)
	return decoder
}

// testDeclarations is the generated test.v1 schema plus a hand-written weather module.
func testDeclarations(t *testing.T) map[string]string {
	t.Helper()
	declarations, err := descriptors.Declarations(t.Context(), descriptorSet())
	assert.NoError(t, err)
	declarations["weather.d.ts"] = `
declare module "weather" {
  import type { City, Filter } from "test.v1";
  interface HTTPQuery {
    location?: string; days?: number; units?: "UNITS_UNSPECIFIED" | "METRIC";
    tags?: string[]; filter?: Filter; trace?: string;
    city?: City; code?: string;
  }
}
`
	return declarations
}

// module imports the spectre API and the test schema modules, so body can name their types.
func module(body string) string {
	return `import * as spectre from "spectre"; import type * as v1 from "test.v1"; import type * as weather from "weather";` + body
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, source := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		assert.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		assert.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	}
	return dir
}

func forecastRequest(location, query string) message.Request {
	return message.Request{
		Method:   http.MethodGet,
		Host:     "weather.example",
		Path:     "/v1/locations/" + location + "/forecast",
		RawQuery: query,
		Header:   http.Header{},
	}
}

func searchRequest(body string) message.Request {
	return message.Request{
		Method: http.MethodPost,
		Host:   "weather.example:8080",
		Path:   "/v1/search",
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   []byte(body),
	}
}

func apiRequest(path, query string) message.Request {
	return message.Request{Method: http.MethodGet, Host: "api.example", Path: path, RawQuery: query, Header: http.Header{}}
}

func protobufRequest(payload []byte) message.Request {
	return message.Request{
		Method: http.MethodPost,
		Path:   "/test.v1.Service/Find",
		Header: http.Header{"Content-Type": []string{"application/x-protobuf"}},
		Body:   payload,
	}
}

func rpcRequest(method, body string) message.Request {
	return message.Request{
		Method: http.MethodPost,
		Host:   "rpc.example",
		Path:   "/test.v1.Service/" + method,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   []byte(body),
	}
}

func grpcRequest(t *testing.T, method string, payload []byte) message.Request {
	t.Helper()
	framed := grpcResponse(t, payload, true)
	return message.Request{
		Method: http.MethodPost,
		Host:   "rpc.example",
		Path:   "/test.v1.Service/" + method,
		Header: framed.Header,
		Body:   framed.Body,
	}
}

func withQuery(request message.Request, query string) message.Request {
	request.RawQuery = query
	return request
}

func withContentType(request message.Request, contentType string) message.Request {
	request.Header = http.Header{"Content-Type": []string{contentType}}
	return request
}

func withHost(request message.Request, host string) message.Request {
	request.Host = host
	return request
}

func queryProto(location string, days uint64) []byte {
	data := protowire.AppendTag(nil, 1, protowire.BytesType)
	data = protowire.AppendString(data, location)
	data = protowire.AppendTag(data, 2, protowire.VarintType)
	return protowire.AppendVarint(data, days)
}

func connectResponse(body string) message.Response {
	return httpResponse(http.StatusOK, "application/json", body)
}

func httpResponse(status int, contentType, body string) message.Response {
	return message.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       []byte(body),
	}
}

func gzipResponse(t *testing.T, body string) message.Response {
	t.Helper()
	response := connectResponse("")
	response.Header.Set("Content-Encoding", "gzip")
	response.Body = gzipBytes(t, []byte(body))
	return response
}

func protobufResponse(payload []byte) message.Response {
	return message.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/x-protobuf"}},
		Body:       payload,
	}
}

func grpcResponse(t *testing.T, payload []byte, compressed bool) message.Response {
	t.Helper()
	flag := byte(0)
	if compressed {
		flag = 1
		payload = gzipBytes(t, payload)
	}
	body := make([]byte, 5, 5+len(payload))
	body[0] = flag
	binary.BigEndian.PutUint32(body[1:], uint32(len(payload)))
	body = append(body, payload...)
	header := http.Header{
		"Content-Type": []string{"application/grpc"},
		"Grpc-Status":  []string{"0"},
	}
	if compressed {
		header.Set("Grpc-Encoding", "gzip")
	}
	return message.Response{StatusCode: http.StatusOK, Header: header, Body: body}
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	_, err := writer.Write(data)
	assert.NoError(t, err)
	assert.NoError(t, writer.Close())
	return buffer.Bytes()
}

func responseProto(stable, ignored string, roles []string) []byte {
	data := protowire.AppendTag(nil, 1, protowire.BytesType)
	data = protowire.AppendString(data, stable)
	if ignored != "" {
		data = protowire.AppendTag(data, 2, protowire.BytesType)
		data = protowire.AppendString(data, ignored)
	}
	for _, role := range roles {
		data = protowire.AppendTag(data, 3, protowire.BytesType)
		data = protowire.AppendString(data, role)
	}
	return data
}

func responseProtoWithLabel(value string) []byte {
	entry := protowire.AppendTag(nil, 1, protowire.BytesType)
	entry = protowire.AppendString(entry, "team")
	entry = protowire.AppendTag(entry, 2, protowire.BytesType)
	entry = protowire.AppendString(entry, value)
	data := protowire.AppendTag(nil, 6, protowire.BytesType)
	return protowire.AppendBytes(data, entry)
}

func descriptorSet() *descriptorpb.FileDescriptorSet {
	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name:    new("comparison.proto"),
		Package: new("test.v1"),
		Syntax:  new("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: new("Units"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: new("UNITS_UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: new("METRIC"), Number: proto.Int32(1)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("Request")},
			withProto3Optional(&descriptorpb.DescriptorProto{
				Name: new("Filter"),
				Field: []*descriptorpb.FieldDescriptorProto{{
					Name:   new("min_days"),
					Number: proto.Int32(1),
					Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
				}},
			}),
			withProto3Optional(&descriptorpb.DescriptorProto{
				Name: new("City"),
				Field: []*descriptorpb.FieldDescriptorProto{{
					Name:   new("name"),
					Number: proto.Int32(1),
					Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				}},
			}),
			withProto3Optional(&descriptorpb.DescriptorProto{
				Name:      new("Query"),
				OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: new("place")}},
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   new("location"),
						Number: proto.Int32(1),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
					{
						Name:   new("days"),
						Number: proto.Int32(2),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
					},
					{
						Name:     new("units"),
						Number:   proto.Int32(3),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
						TypeName: new(".test.v1.Units"),
					},
					{
						Name:   new("tags"),
						Number: proto.Int32(4),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
					{
						Name:     new("filter"),
						Number:   proto.Int32(5),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: new(".test.v1.Filter"),
					},
					{
						Name:   new("trace"),
						Number: proto.Int32(6),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
					{
						Name:       new("city"),
						Number:     proto.Int32(7),
						Label:      descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:       descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName:   new(".test.v1.City"),
						OneofIndex: proto.Int32(0),
					},
					{
						Name:       new("code"),
						Number:     proto.Int32(8),
						Label:      descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:       descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						OneofIndex: proto.Int32(0),
					},
				},
			}),
			{
				Name: new("User"),
				Field: []*descriptorpb.FieldDescriptorProto{{
					Name:   new("name"),
					Number: proto.Int32(1),
					Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				}},
			},
			{
				Name:      new("Response"),
				OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: new("_ignored")}},
				NestedType: []*descriptorpb.DescriptorProto{{
					Name:    new("LabelsEntry"),
					Options: &descriptorpb.MessageOptions{MapEntry: new(true)},
					Field: []*descriptorpb.FieldDescriptorProto{
						{
							Name:   new("key"),
							Number: proto.Int32(1),
							Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
							Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						},
						{
							Name:   new("value"),
							Number: proto.Int32(2),
							Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
							Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						},
					},
				}},
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   new("stable"),
						Number: proto.Int32(1),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
					{
						Name:           new("ignored"),
						Number:         proto.Int32(2),
						Label:          descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:           descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						Proto3Optional: new(true),
						OneofIndex:     proto.Int32(0),
					},
					{
						Name:   new("roles"),
						Number: proto.Int32(3),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
					{
						Name:     new("users"),
						Number:   proto.Int32(4),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: new(".test.v1.User"),
					},
					{
						Name:   new("count"),
						Number: proto.Int32(5),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum(),
					},
					{
						Name:     new("owner"),
						Number:   proto.Int32(7),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: new(".test.v1.User"),
					},
					{
						Name:     new("labels"),
						Number:   proto.Int32(6),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: new(".test.v1.Response.LabelsEntry"),
					},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: new("Service"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{
					Name:       new("Get"),
					InputType:  new(".test.v1.Request"),
					OutputType: new(".test.v1.Response"),
				},
				{
					Name:       new("Find"),
					InputType:  new(".test.v1.Query"),
					OutputType: new(".test.v1.Response"),
				},
				{
					Name:       new("Rename"),
					InputType:  new(".test.v1.User"),
					OutputType: new(".test.v1.Response"),
				},
				{
					Name:            new("Watch"),
					InputType:       new(".test.v1.Request"),
					OutputType:      new(".test.v1.Response"),
					ServerStreaming: new(true),
				},
			},
		}},
	}}}
}

// withProto3Optional gives each singular scalar outside a oneof explicit presence.
func withProto3Optional(descriptor *descriptorpb.DescriptorProto) *descriptorpb.DescriptorProto {
	for _, field := range descriptor.GetField() {
		if field.OneofIndex != nil || field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE ||
			field.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
			continue
		}
		field.Proto3Optional = new(true)
		field.OneofIndex = new(int32(len(descriptor.GetOneofDecl())))
		descriptor.OneofDecl = append(descriptor.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: new("_" + field.GetName())})
	}
	return descriptor
}

package message_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/protocols/http/message"
	"github.com/block/spectre/internal/protocols/http/script"
	"github.com/block/spectre/internal/schema"
)

// decoded is one response's comparable status and message, or why it failed to decode.
type decoded struct {
	status  Option[string]
	message Option[typedValue]
	failure string
	ignored Option[string]
}

func TestDecodesRPCResponses(t *testing.T) {
	decoder := newDecoder(t, "")
	defaults := responseValue(map[string]any{})
	for name, test := range map[string]struct {
		contentType string
		response    message.Response
		expected    decoded
	}{
		"ConnectJSONEmitsDefaults": {
			contentType: "application/json",
			response:    connectResponse(`{}`),
			expected:    decoded{status: Some("200"), message: typed("test.v1.Response", defaults)},
		},
		"ConnectJSONExplicitDefaults": {
			contentType: "application/json",
			response:    connectResponse(`{"stable":"","roles":[],"users":[],"count":"0","labels":{}}`),
			expected:    decoded{status: Some("200"), message: typed("test.v1.Response", defaults)},
		},
		"ConnectJSONInt64IsString": {
			contentType: "application/json",
			response:    connectResponse(`{"count":"9007199254740993"}`),
			expected:    decoded{status: Some("200"), message: typed("test.v1.Response", responseValue(map[string]any{"count": "9007199254740993"}))},
		},
		// Connect error envelopes are not instances of the RPC output message.
		"ConnectErrorIsTyped": {
			contentType: "application/json",
			response:    httpResponse(http.StatusNotFound, "application/json", `{"code":"not_found"}`),
			expected: decoded{status: Some("404"), message: Some(typedValue{
				declared: script.ConnectError + " | null",
				value:    map[string]any{"code": "not_found"},
			})},
		},
		"ConnectErrorInvalid": {
			contentType: "application/json",
			response:    httpResponse(http.StatusNotFound, "application/json", `{"code":"lost"}`),
			expected:    decoded{status: Some("404"), failure: "validate Connect error response"},
		},
		"ConnectResponseNotJSON": {
			contentType: "application/json",
			response:    httpResponse(http.StatusOK, "text/html", `<html></html>`),
			expected:    decoded{status: Some("200"), failure: `Connect response is not JSON: "text/html"`},
		},
		"GRPCEmitsDefaults": {
			contentType: "application/grpc",
			response:    grpcResponse(t, nil, false),
			expected:    decoded{status: Some("grpc-status 0"), message: typed("test.v1.Response", defaults)},
		},
		"GRPCExplicitDefaults": {
			contentType: "application/grpc",
			response:    grpcResponse(t, responseProto("", "", nil), false),
			expected:    decoded{status: Some("grpc-status 0"), message: typed("test.v1.Response", defaults)},
		},
		"GRPCCompressed": {
			contentType: "application/grpc",
			response:    grpcResponse(t, responseProto("same", "first", []string{"reader"}), true),
			expected: decoded{status: Some("grpc-status 0"), message: typed("test.v1.Response", responseValue(map[string]any{
				"stable": "same", "ignored": "first", "roles": []any{"reader"},
			}))},
		},
		"GRPCProto": {
			contentType: "application/grpc+proto",
			response:    grpcResponse(t, responseProto("same", "", nil), false),
			expected:    decoded{status: Some("grpc-status 0"), message: typed("test.v1.Response", responseValue(map[string]any{"stable": "same"}))},
		},
		"GRPCScalarMap": {
			contentType: "application/grpc",
			response:    grpcResponse(t, responseProtoWithLabel("reference"), false),
			expected: decoded{status: Some("grpc-status 0"), message: typed("test.v1.Response", responseValue(map[string]any{
				"labels": map[string]any{"team": "reference"},
			}))},
		},
		// A non-OK gRPC status carries no output message, so only the status compares.
		"GRPCErrorStatus": {
			contentType: "application/grpc",
			response:    withHeader(grpcResponse(t, responseProto("same", "", nil), false), "Grpc-Status", "7"),
			expected:    decoded{status: Some("grpc-status 7"), message: None[typedValue]()},
		},
		"GRPCInvalidStatus": {
			contentType: "application/grpc",
			response:    withHeader(grpcResponse(t, responseProto("same", "", nil), false), "Grpc-Status", "invalid"),
			expected:    decoded{failure: "parse gRPC status"},
		},
		"ProtobufEmitsDefaults": {
			contentType: "application/x-protobuf",
			response:    protobufResponse(nil),
			expected:    decoded{status: Some("200"), message: typed("test.v1.Response", defaults)},
		},
		"ProtobufExplicitDefaults": {
			contentType: "application/x-protobuf",
			response:    protobufResponse(responseProto("", "", nil)),
			expected:    decoded{status: Some("200"), message: typed("test.v1.Response", defaults)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assertDecodes(t, decoder, http.MethodPost, "/test.v1.Service/Get", test.contentType, test.response, test.expected)
		})
	}
}

func TestDecodesConnectGetResponses(t *testing.T) {
	decoder := newDecoder(t, "")
	plan := decoder.Responses(http.MethodGet, "/test.v1.Service/Get", "encoding=json&message=%7B%7D", http.Header{})
	assert.Equal(t, None[string](), plan.Ignored())

	assert.Equal(t, decoded{status: Some("200"), message: typed("test.v1.Response", responseValue(map[string]any{"stable": "same"}))},
		describe(t, plan.Decode(t.Context(), connectResponse(`{"stable":"same"}`))))
}

func TestDecodesResponseStatus(t *testing.T) {
	decoder := newDecoder(t, weatherScripts)
	grpcOK := grpcResponse(t, responseProto("same", "", nil), false)
	for name, test := range map[string]struct {
		method      string
		path        string
		contentType string
		response    message.Response
		expected    decoded
	}{
		// Every non-gRPC wire carries the HTTP status, even when the body fails to decode.
		"ConnectBodyFails": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/json",
			response: httpResponse(http.StatusInternalServerError, "text/html", `<html></html>`),
			expected: decoded{status: Some("500"), failure: `Connect response is not JSON: "text/html"`},
		},
		"DeclaredHTTPBodyFails": {
			method: http.MethodGet, path: "/v1/forecast",
			response: httpResponse(http.StatusBadGateway, "application/json", `{"unknown":1}`),
			expected: decoded{status: Some("502"), failure: `has a field it does not declare`},
		},
		"ProtobufBodyFails": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/x-protobuf",
			response: httpResponse(http.StatusOK, "text/plain", ""),
			expected: decoded{status: Some("200"), failure: `protobuf response has an invalid content type: "text/plain"`},
		},
		"UnresolvedPlan": {
			method: http.MethodPost, path: "/test.v1.Service/Missing", contentType: "application/json",
			response: httpResponse(http.StatusServiceUnavailable, "text/plain", "unavailable"),
			expected: decoded{status: Some("503"), failure: `operation "test.v1.Service.Missing" is not declared`},
		},
		"GRPCOK": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/grpc", response: grpcOK,
			expected: decoded{status: Some("grpc-status 0"), message: typed("test.v1.Response", responseValue(map[string]any{"stable": "same"}))},
		},
		"GRPCErrorStatus": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/grpc",
			response: withHeader(grpcOK, "Grpc-Status", "14"),
			expected: decoded{status: Some("grpc-status 14")},
		},
		// Without an HTTP 200 there is no gRPC status, so the HTTP status stands in for it.
		"GRPCNon200": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/grpc",
			response: withStatus(grpcOK, http.StatusServiceUnavailable),
			expected: decoded{status: Some("503"), failure: "gRPC response has HTTP status 503"},
		},
		"GRPCInvalidContentType": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/grpc",
			response: withHeader(grpcOK, "Content-Type", "text/html"),
			expected: decoded{failure: `gRPC response has an invalid content type: "text/html"`},
		},
		"GRPCInvalidStatus": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/grpc",
			response: withHeader(grpcOK, "Grpc-Status", "-1"),
			expected: decoded{failure: "gRPC status is negative: -1"},
		},
		// A plan that failed to resolve has no wire, so even gRPC compares the HTTP status.
		"GRPCUnresolvedPlan": {
			method: http.MethodPost, path: "/test.v1.Service/Missing", contentType: "application/grpc", response: grpcOK,
			expected: decoded{status: Some("200"), failure: "RPC method is absent from the comparison schema"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assertDecodes(t, decoder, test.method, test.path, test.contentType, test.response, test.expected)
		})
	}
}

func TestDecodesDeclaredHTTPJSONResponses(t *testing.T) {
	decoder := newDecoder(t, weatherScripts)
	body := `{"stable":"same","ignored":"first","roles":["a","b"],"users":[],"count":"0","labels":{}}`
	value := map[string]any{"stable": "same", "ignored": "first", "roles": []any{"a", "b"}, "users": []any{}, "count": "0", "labels": map[string]any{}}
	for name, test := range map[string]struct {
		response message.Response
		expected decoded
	}{
		"OK": {
			response: httpResponse(http.StatusOK, "application/json; charset=utf-8", body),
			expected: decoded{status: Some("200"), message: typed("test.v1.Response", value)},
		},
		"ErrorStatusUsesDeclaredType": {
			response: httpResponse(http.StatusUnauthorized, "application/json", body),
			expected: decoded{status: Some("401"), message: typed("test.v1.Response", value)},
		},
		"NoContent": {
			response: httpResponse(http.StatusNoContent, "", ""),
			expected: decoded{status: Some("204"), message: None[typedValue]()},
		},
		"EmptyBodyIsValidated": {
			response: httpResponse(http.StatusOK, "", ""),
			expected: decoded{status: Some("200"), failure: `decode response as test.v1.Response: validate response: $: required field "stable" of type "test.v1.Response" is missing`},
		},
		"NotJSON": {
			response: httpResponse(http.StatusInternalServerError, "text/html", `<html></html>`),
			expected: decoded{status: Some("500"), failure: `decode response as test.v1.Response: body is not JSON: "text/html"`},
		},
		"UnknownField": {
			response: httpResponse(http.StatusOK, "application/json", `{"stable":"same","roles":[],"users":[],"count":"0","labels":{},"added":true}`),
			expected: decoded{status: Some("200"), failure: `type "test.v1.Response" has a field it does not declare`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assertDecodes(t, decoder, http.MethodGet, "/v1/forecast", "", test.response, test.expected)
		})
	}
}

// Decode failures become quarantine reasons, which leave the mirror.
func TestDecodeFailuresOmitPayloadValues(t *testing.T) {
	decoder := newDecoder(t, weatherScripts)
	for name, test := range map[string]struct {
		method      string
		path        string
		contentType string
		response    message.Response
		expected    decoded
	}{
		"ProtoJSONValue": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/json",
			response: connectResponse(`{"count":"secret"}`),
			expected: decoded{status: Some("200"), failure: "decode response as test.v1.Response: decode wire payload: invalid ProtoJSON for test.v1.Response"},
		},
		"ConnectErrorJSON": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/json",
			response: httpResponse(http.StatusNotFound, "application/json", `{"code":secret}`),
			expected: decoded{status: Some("404"), failure: "Connect error response contains malformed JSON: decode Connect error JSON: invalid JSON at offset 9"},
		},
		"GRPCStatus": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/grpc",
			response: withHeader(grpcResponse(t, responseProto("same", "", nil), false), "Grpc-Status", "secret"),
			expected: decoded{failure: "response has no valid gRPC status: parse gRPC status: Atoi: invalid syntax"},
		},
		"HTTPJSON": {
			method: http.MethodGet, path: "/v1/forecast",
			response: httpResponse(http.StatusOK, "application/json", `{"stable":secret}`),
			expected: decoded{status: Some("200"), failure: "decode response as test.v1.Response: decode JSON: invalid JSON at offset 11"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			header := http.Header{}
			if test.contentType != "" {
				header.Set("Content-Type", test.contentType)
			}
			content := decoder.Responses(test.method, test.path, "", header).Decode(t.Context(), test.response)
			assert.Equal(t, test.expected, describe(t, content))
		})
	}
}

func TestDeclaredEndpointsSelectWireByContentType(t *testing.T) {
	for name, test := range map[string]struct {
		path        string
		contentType string
		response    message.Response
		status      string
	}{
		"GRPC":          {path: "/v1/forecast", contentType: "application/grpc", response: grpcResponse(t, responseProto("first", "", nil), false), status: "grpc-status 0"},
		"Protobuf":      {path: "/v1/forecast", contentType: "application/x-protobuf", response: protobufResponse(responseProto("first", "", nil)), status: "200"},
		"MissingMethod": {path: "/test.v1.Service/Missing", contentType: "application/grpc", response: grpcResponse(t, responseProto("first", "", nil), false), status: "grpc-status 0"},
	} {
		t.Run(name, func(t *testing.T) {
			decoder := newDecoder(t, `spectre.ingress.http("POST `+test.path+`").match<v1.Response>();`)

			expected := decoded{status: Some(test.status), message: typed("test.v1.Response", responseValue(map[string]any{"stable": "first"}))}
			assertDecodes(t, decoder, http.MethodPost, test.path, test.contentType, test.response, expected)
		})
	}
}

func TestRulesSelectEachResponse(t *testing.T) {
	decoder := newDecoder(t, `
		spectre.ingress.http("GET /_status").ignore();
		spectre.ingress.http("/").status(500).match<spectre.Text>(({ status }) => ({ status }));
		spectre.ingress.http((request) => request.path.startsWith("/v2/")).status((status) => status === 503).ignore();
		spectre.ingress.http("GET /v3/forecast").match<unknown>();
		spectre.ingress.http("POST /test.v1.Service/Find").match<unknown>();
		spectre.ingress.http("GET /v1/forecast").responseHeader("X-Skip", "yes").ignore();
		spectre.ingress.http("GET /v1/forecast").match<v1.Response>();
		spectre.ingress.http("GET /v2/forecast").match<v1.Response>();
	`)
	body := `{"stable":"same","roles":[],"users":[],"count":"0","labels":{}}`
	forecast := typed("test.v1.Response", responseValue(map[string]any{"stable": "same"}))
	for name, test := range map[string]struct {
		method      string
		path        string
		contentType string
		response    message.Response
		expected    decoded
		rule        string
	}{
		"StatusRule": {
			method: http.MethodGet, path: "/v1/forecast", response: httpResponse(http.StatusInternalServerError, "text/html", "<html></html>"),
			expected: decoded{status: Some("500"), message: Some(typedValue{declared: "string", value: "<html></html>"})},
			rule:     `http("/").status(500)`,
		},
		"RouteRule": {
			method: http.MethodGet, path: "/v1/forecast", response: httpResponse(http.StatusOK, "application/json", body),
			expected: decoded{status: Some("200"), message: forecast},
			rule:     `http("GET /v1/forecast")`,
		},
		"HeaderIgnores": {
			method: http.MethodGet, path: "/v1/forecast", response: withHeader(httpResponse(http.StatusOK, "application/json", body), "X-Skip", "yes"),
			expected: decoded{status: Some("200"), ignored: Some(`rule http("GET /v1/forecast").responseHeader("X-Skip", "yes") ignores the response`)},
		},
		"HeaderDiffers": {
			method: http.MethodGet, path: "/v1/forecast", response: withHeader(httpResponse(http.StatusOK, "application/json", body), "X-Skip", "no"),
			expected: decoded{status: Some("200"), message: forecast},
			rule:     `http("GET /v1/forecast")`,
		},
		"FunctionsIgnore": {
			method: http.MethodGet, path: "/v2/forecast", response: httpResponse(http.StatusServiceUnavailable, "text/plain", "down"),
			expected: decoded{status: Some("503"), ignored: Some("rule http(<function>).status(<function>) ignores the response")},
		},
		"FunctionsReject": {
			method: http.MethodGet, path: "/v2/forecast", response: httpResponse(http.StatusOK, "application/json", body),
			expected: decoded{status: Some("200"), message: forecast},
			rule:     `http("GET /v2/forecast")`,
		},
		"AnyMethod": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/json",
			response: httpResponse(http.StatusInternalServerError, "text/html", "<html></html>"),
			expected: decoded{status: Some("500"), message: Some(typedValue{declared: "string", value: "<html></html>"})},
			rule:     `http("/").status(500)`,
		},
		"NoRuleSelects": {
			method: http.MethodPost, path: "/test.v1.Service/Get", contentType: "application/json",
			response: connectResponse(body),
			expected: decoded{status: Some("200"), message: forecast},
		},
		"NoRuleSelectsUnsupportedRequest": {
			method: http.MethodPost, path: "/v1/forecast", response: httpResponse(http.StatusOK, "application/json", body),
			expected: decoded{status: Some("200"), failure: "undeclared request: request content type is not supported: mime: no media type"},
		},
		"EmptyText": {
			method: http.MethodHead, path: "/v1/forecast", response: httpResponse(http.StatusInternalServerError, "text/html", ""),
			expected: decoded{status: Some("500")},
			rule:     `http("/").status(500)`,
		},
		"TextNotUTF8": {
			method: http.MethodGet, path: "/v1/forecast", response: httpResponse(http.StatusInternalServerError, "text/html", "\xff"),
			expected: decoded{status: Some("500"), failure: "Text body is not UTF-8"},
			rule:     `http("/").status(500)`,
		},
		"Unknown": {
			method: http.MethodGet, path: "/v3/forecast", response: httpResponse(http.StatusOK, "application/json", `{"any":[1,"two"]}`),
			expected: decoded{status: Some("200"), message: Some(typedValue{
				declared: "unknown",
				value:    map[string]any{"any": []any{1.0, "two"}},
			})},
			rule: `http("GET /v3/forecast")`,
		},
		"UnknownProtobuf": {
			method: http.MethodPost, path: "/test.v1.Service/Find", contentType: "application/x-protobuf",
			response: protobufResponse(responseProto("first", "", nil)),
			expected: decoded{status: Some("200"), failure: "protobuf responses cannot be decoded as unknown"},
			rule:     `http("POST /test.v1.Service/Find")`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			header := http.Header{}
			if test.contentType != "" {
				header.Set("Content-Type", test.contentType)
			}
			plan := decoder.Responses(test.method, test.path, "", header)
			assert.Equal(t, None[string](), plan.Ignored())

			content := plan.Decode(t.Context(), test.response)

			assert.Equal(t, test.expected, describe(t, content))
			assert.Equal(t, test.rule != "", content.Normalise.Ok())
		})
	}
}

func TestRulesNormaliseResponses(t *testing.T) {
	decoder := newDecoder(t, `
		spectre.ingress.http("GET /status").match<spectre.Text>(({ status, body }) => ({ status: status === 201 ? 200 : status, body }));
		spectre.ingress.http("GET /body").match<spectre.Text>(({ body }) => ({ body }));
		spectre.ingress.http("GET /plain").match<spectre.Text>();
		spectre.ingress.http("POST /test.v1.Service/Find").match<v1.Response>(({ body }) => ({ body }));
	`)
	text := func(value string) Option[typedValue] { return Some(typedValue{declared: "string", value: value}) }
	for name, test := range map[string]struct {
		method      string
		path        string
		contentType string
		response    message.Response
		status      Option[string]
		message     Option[typedValue]
		failure     string
	}{
		"StatusIsMapped": {
			method: http.MethodGet, path: "/status", response: httpResponse(http.StatusCreated, "text/plain", "same"),
			status: Some("200"), message: text("same"),
		},
		"StatusIsKept": {
			method: http.MethodGet, path: "/status", response: httpResponse(http.StatusInternalServerError, "text/plain", "same"),
			status: Some("500"), message: text("same"),
		},
		"StatusIsDropped": {
			method: http.MethodGet, path: "/body", response: httpResponse(http.StatusCreated, "text/plain", "same"),
			message: text("same"),
		},
		"NormaliserNeedsHTTPStatus": {
			method: http.MethodPost, path: "/test.v1.Service/Find", contentType: "application/grpc",
			response: grpcResponse(t, nil, false), failure: `rule http("POST /test.v1.Service/Find") needs an HTTP status`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			header := http.Header{}
			if test.contentType != "" {
				header.Set("Content-Type", test.contentType)
			}
			content := decoder.Responses(test.method, test.path, "", header).Decode(t.Context(), test.response)
			normalise, ok := content.Normalise.Get()
			assert.True(t, ok)

			status, normalised, err := normalise(t.Context())

			if test.failure != "" {
				assert.EqualError(t, err, test.failure)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.status, status)
			assert.Equal(t, test.message, typedValues(t, normalised))
		})
	}
}

func TestRulesWithoutNormalisersKeepResponses(t *testing.T) {
	decoder := newDecoder(t, `spectre.ingress.http("GET /plain").match<spectre.Text>();`)

	content := decoder.Responses(http.MethodGet, "/plain", "", http.Header{}).Decode(t.Context(), httpResponse(http.StatusCreated, "text/plain", "same"))
	status, normalised, err := content.Normalise.MustGet()(t.Context())

	assert.NoError(t, err)
	assert.Equal(t, Some("201"), status)
	assert.Equal(t, content.Message, normalised)
}

func TestRequestHeaderRulesIgnoreRequestsUpFront(t *testing.T) {
	decoder := newDecoder(t, `
		spectre.ingress.http("GET /v1/forecast").requestHeader("X-Synthetic", "true").ignore();
		spectre.ingress.http("GET /v2/forecast").requestHeader("X-Synthetic", (value) => value === "true").ignore();
		spectre.ingress.http("/").match<spectre.Text>();
	`)
	for name, test := range map[string]struct {
		path      string
		synthetic string
		expected  Option[string]
	}{
		"Value":          {path: "/v1/forecast", synthetic: "true", expected: Some(`rule http("GET /v1/forecast").requestHeader("X-Synthetic", "true") ignores the request`)},
		"OtherValue":     {path: "/v1/forecast", synthetic: "false", expected: None[string]()},
		"FunctionsLater": {path: "/v2/forecast", synthetic: "true", expected: None[string]()},
	} {
		t.Run(name, func(t *testing.T) {
			header := http.Header{"X-Synthetic": {test.synthetic}}
			assert.Equal(t, test.expected, decoder.Responses(http.MethodGet, test.path, "", header).Ignored())
		})
	}
	content := decoder.Responses(http.MethodGet, "/v2/forecast", "", http.Header{"X-Synthetic": {"true"}}).Decode(t.Context(), httpResponse(http.StatusOK, "text/plain", "ok"))
	assert.Equal(t, decoded{status: Some("200"), ignored: Some(`rule http("GET /v2/forecast").requestHeader("X-Synthetic", <function>) ignores the response`)}, describe(t, content))
}

func TestDecodesHTTPJSONWithoutDescriptors(t *testing.T) {
	decoder := newRawDecoder(t)
	for name, test := range map[string]struct {
		path     string
		response message.Response
		expected decoded
	}{
		"OptionalFieldsMayBeAbsent": {
			path:     "/raw",
			response: connectResponse(`{"stable":"same"}`),
			expected: decoded{status: Some("200"), message: typed("raw.Response", map[string]any{"stable": "same"})},
		},
		"ExplicitDefaultsRemainPresent": {
			path:     "/raw",
			response: connectResponse(`{"stable":"same","ignored":"","count":0,"enabled":false,"roles":[],"metadata":{}}`),
			expected: decoded{status: Some("200"), message: typed("raw.Response", map[string]any{
				"stable": "same", "ignored": "", "count": float64(0), "enabled": false, "roles": []any{}, "metadata": map[string]any{},
			})},
		},
		"Gzip": {
			path:     "/raw",
			response: gzipResponse(t, `{"stable":"same"}`),
			expected: decoded{status: Some("200"), message: typed("raw.Response", map[string]any{"stable": "same"})},
		},
		// An unsupported encoding fails clearly, instead of as malformed JSON.
		"UnsupportedEncoding": {
			path:     "/raw",
			response: withHeader(connectResponse("not decodable"), "Content-Encoding", "br"),
			expected: decoded{status: Some("200"), failure: `unsupported Content-Encoding: "br"`},
		},
		"UnknownField": {
			path:     "/raw",
			response: connectResponse(`{"stable":"same","added":true}`),
			expected: decoded{status: Some("200"), failure: `type "raw.Response" has a field it does not declare`},
		},
		"RequiredField": {
			path:     "/raw",
			response: connectResponse(`{}`),
			expected: decoded{status: Some("200"), failure: `required field "stable" of type "raw.Response" is missing`},
		},
		"NullScalar": {
			path:     "/raw",
			response: connectResponse(`{"stable":"same","ignored":null}`),
			expected: decoded{status: Some("200"), failure: "$.ignored: expected string, found null"},
		},
		"NullMessage": {
			path:     "/raw",
			response: connectResponse(`{"stable":"same","owner":null}`),
			expected: decoded{status: Some("200"), failure: "$.owner: expected raw.User, found null"},
		},
		"NullRoot": {
			path:     "/raw",
			response: connectResponse(`null`),
			expected: decoded{status: Some("200"), failure: "$: expected raw.Response, found null"},
		},
		"NoImplicitScalarConversion": {
			path:     "/raw",
			response: connectResponse(`{"stable":"same","count":"0"}`),
			expected: decoded{status: Some("200"), failure: "$.count: expected number, found a string"},
		},
		"EmptyBodyWithRequiredField": {
			path:     "/raw",
			response: connectResponse(""),
			expected: decoded{status: Some("200"), failure: `validate response: $: required field "stable" of type "raw.Response" is missing`},
		},
		// An empty body is an empty object, so normalisers still run on it.
		"EmptyBodyWithOptionalFields": {
			path:     "/optional",
			response: connectResponse(""),
			expected: decoded{status: Some("200"), message: typed("raw.Optional", map[string]any{})},
		},
		"EmptyObjectWithOptionalFields": {
			path:     "/optional",
			response: connectResponse(`{}`),
			expected: decoded{status: Some("200"), message: typed("raw.Optional", map[string]any{})},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assertDecodes(t, decoder, http.MethodGet, test.path, "", test.response, test.expected)
		})
	}
}

func TestDecodesBodylessHTTPResponses(t *testing.T) {
	decoder := newRawDecoder(t)
	for name, test := range map[string]struct {
		method string
		status int
	}{
		"Head":        {method: http.MethodHead, status: http.StatusOK},
		"NoContent":   {method: http.MethodGet, status: http.StatusNoContent},
		"NotModified": {method: http.MethodGet, status: http.StatusNotModified},
	} {
		t.Run(name, func(t *testing.T) {
			assertDecodes(t, decoder, test.method, "/raw", "", httpResponse(test.status, "", ""), decoded{status: Some(strconv.Itoa(test.status)), message: None[typedValue]()})
		})
	}
}

// assertDecodes plans the responses to one request, then decodes response. An
// expected failure need only be contained in the error.
func assertDecodes(t *testing.T, decoder *message.Decoder, method, path, contentType string, response message.Response, expected decoded) {
	t.Helper()
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	content := decoder.Responses(method, path, "", header).Decode(t.Context(), response)
	actual := describe(t, content)
	if err, failed := content.Failure.Get(); failed && expected.failure != "" {
		assert.Contains(t, err.Error(), expected.failure)
		actual.failure = expected.failure
	}
	assert.Equal(t, expected, actual)
}

// describe flattens content so that whole values compare, with the failure as its message.
func describe(t *testing.T, content engine.Content) decoded {
	t.Helper()
	actual := decoded{status: content.Status, message: typedValues(t, content.Message), ignored: content.Ignored}
	if err, failed := content.Failure.Get(); failed {
		actual.failure = err.Error()
	}
	return actual
}

// typedValue is a value's type and JSON, so expectations need no schema to build.
type typedValue struct {
	declared string
	value    any
}

func typedOf(t *testing.T, value schema.Value) typedValue {
	t.Helper()
	data, err := json.Marshal(value)
	assert.NoError(t, err)
	var decoded any
	assert.NoError(t, json.Unmarshal(data, &decoded))
	return typedValue{declared: value.Type().String(), value: decoded}
}

func typedValues(t *testing.T, value Option[schema.Value]) Option[typedValue] {
	t.Helper()
	if message, ok := value.Get(); ok {
		return Some(typedOf(t, message))
	}
	return None[typedValue]()
}

func typed(name schema.TypeRef, value any) Option[typedValue] {
	return Some(typedValue{declared: string(name), value: value})
}

// responseValue is a test.v1.Response in ProtoJSON with defaults emitted.
func responseValue(fields map[string]any) map[string]any {
	value := map[string]any{"stable": "", "roles": []any{}, "users": []any{}, "count": "0", "labels": map[string]any{}}
	for name, field := range fields {
		value[name] = field
	}
	return value
}

func withHeader(response message.Response, name, value string) message.Response {
	response.Header = response.Header.Clone()
	response.Header.Set(name, value)
	return response
}

func withStatus(response message.Response, status int) message.Response {
	response.StatusCode = status
	return response
}

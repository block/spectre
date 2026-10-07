package comparison_test

import (
	"bytes"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison"
)

const egressScript = `
	spectre.egress.match<weather.HTTPQuery>("http", "GET weather.example/v1/locations/{location}/forecast");
	spectre.egress.match<weather.HTTPQuery>("http", "POST weather.example/v1/search");
	spectre.field<v1.Query, "trace">(() => undefined);
	spectre.field<v1.Query, "tags">((tags) => tags === undefined ? undefined : tags.sort());
	spectre.field<weather.HTTPQuery, "trace">(() => undefined);
	spectre.field<weather.HTTPQuery, "tags">((tags) => tags === undefined ? undefined : tags.sort());
`

func TestHashesMethodAndCanonicalJSON(t *testing.T) {
	hasher := newHasher(t, egressScript)

	hash, err := hasher.Hash(t.Context(), searchRequest(`{"trace":"ignored","location":"london","days":3}`))

	assert.NoError(t, err)
	expected := sha256.Sum256([]byte("POST weather.example/v1/search\x00" + `{"days":3,"location":"london"}`))
	assert.Equal(t, comparison.RequestHash(expected), hash)
}

func TestLogsRequestNormalisationWithoutNormalisers(t *testing.T) {
	var output bytes.Buffer
	config := newConfig(t, map[string]string{"test.ts": module(`
		spectre.egress.match<weather.HTTPQuery>("http", "POST weather.example/v1/search");
	`)})
	log := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	hasher, err := comparison.NewRequestHasher(t.Context(), config, log)
	assert.NoError(t, err)
	assert.NoError(t, hasher.Configure(t.Context(), descriptorSet()))

	request := searchRequest(`{"location":"london"}`)
	request.Side = "candidate"

	_, err = hasher.Hash(t.Context(), request)

	assert.NoError(t, err)
	assert.Contains(t, output.String(), `"level":"DEBUG","msg":"Payload normalisation completed","message":"weather.HTTPQuery","side":"candidate","normalisers":0`)
}

func TestRequestHashIsStable(t *testing.T) {
	hasher := newHasher(t, egressScript)
	for name, test := range map[string]struct {
		left  comparison.Request
		right comparison.Request
		equal bool
	}{
		"FieldOrder": {
			left:  searchRequest(`{"location":"london","days":3}`),
			right: searchRequest(`{"days":3,"location":"london"}`),
			equal: true,
		},
		"IgnoredField": {
			left:  searchRequest(`{"location":"london","trace":"first"}`),
			right: searchRequest(`{"location":"london","trace":"second"}`),
			equal: true,
		},
		"SortedCollection": {
			left:  searchRequest(`{"tags":["wind","rain"]}`),
			right: searchRequest(`{"tags":["rain","wind"]}`),
			equal: true,
		},
		"QueryOrder": {
			left:  forecastRequest("london", "days=3&units=METRIC&tags=wind&tags=rain"),
			right: forecastRequest("london", "tags=rain&units=METRIC&tags=wind&days=3"),
			equal: true,
		},
		"PathQueryAndBody": {
			left:  forecastRequest("london", "days=3&tags=wind&filter.minDays=2"),
			right: searchRequest(`{"location":"london","days":3,"tags":["wind"],"filter":{"minDays":2}}`),
		},
		"ExplicitDefault": {
			left:  forecastRequest("london", ""),
			right: forecastRequest("london", "days=0"),
		},
		"EscapedWildcard": {
			left:  forecastRequest("new%20york", ""),
			right: forecastRequest("new york", ""),
			equal: true,
		},
		"ConnectAndGRPC": {
			left:  rpcRequest("Find", `{"location":"london","days":3}`),
			right: grpcRequest(t, "Find", queryProto("london", 3)),
			equal: true,
		},
		"ProtobufMatchesJSON": {
			left:  rpcRequest("Find", `{"location":"london","days":3}`),
			right: protobufRequest(queryProto("london", 3)),
			equal: true,
		},
		"Value": {
			left:  searchRequest(`{"location":"london","days":3}`),
			right: searchRequest(`{"location":"london","days":4}`),
		},
		"Method": {
			left:  rpcRequest("Find", `{}`),
			right: rpcRequest("Get", `{}`),
		},
	} {
		t.Run(name, func(t *testing.T) {
			left, err := hasher.Hash(t.Context(), test.left)
			assert.NoError(t, err)
			right, err := hasher.Hash(t.Context(), test.right)
			assert.NoError(t, err)
			assert.Equal(t, test.equal, left == right)
		})
	}
}

func TestRejectsUnidentifiableRequests(t *testing.T) {
	hasher := newHasher(t, egressScript)
	overflow := searchRequest(`{}`)
	overflow.Overflow = true
	getWithBody := withContentType(forecastRequest("london", ""), "application/json")
	getWithBody.Body = []byte(`{}`)
	for name, test := range map[string]struct {
		request comparison.Request
		message string
	}{
		"UnknownQueryParameter": {request: forecastRequest("london", "unknown=1"), message: `bind query parameter "unknown": type "weather.HTTPQuery" has no field "unknown"`},
		"UnknownBodyField":      {request: searchRequest(`{"unknown":1}`), message: `has no field "unknown"`},
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
			_, err := hasher.Hash(t.Context(), test.request)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestHasherRequiresSchema(t *testing.T) {
	config := newConfig(t, map[string]string{"test.ts": module(egressScript)})
	hasher, err := comparison.NewRequestHasher(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)

	_, err = hasher.Hash(t.Context(), searchRequest(`{}`))

	assert.EqualError(t, err, "comparison schema is not ready")
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
			config := newConfig(t, map[string]string{
				"weather.ts": module(`spectre.egress.match<weather.HTTPQuery>("http", "` + test.pattern + `");`),
			})
			hasher, err := comparison.NewRequestHasher(t.Context(), config, slog.New(slog.DiscardHandler))
			assert.NoError(t, err)
			err = hasher.Configure(t.Context(), descriptorSet())
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `bind endpoint "`+test.pattern+`"`)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestHasherIgnoresIngressEndpoints(t *testing.T) {
	// An ingress type absent from the schema would fail Configure if egress used it. The
	// cast skips type checking, so the type reaches Configure.
	hasher := newHasher(t, `(spectre.ingress.match as any)("Missing", "http", "GET /v1/forecast");`)

	_, err := hasher.Hash(t.Context(), rpcRequest("Find", `{}`))

	assert.NoError(t, err)
}

func newHasher(t *testing.T, script string) *comparison.RequestHasher {
	t.Helper()
	config := newConfig(t, map[string]string{"test.ts": module(script)})
	hasher, err := comparison.NewRequestHasher(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	assert.NoError(t, hasher.Configure(t.Context(), descriptorSet()))
	return hasher
}

func forecastRequest(location, query string) comparison.Request {
	return comparison.Request{
		Method:   http.MethodGet,
		Host:     "weather.example",
		Path:     "/v1/locations/" + location + "/forecast",
		RawQuery: query,
		Header:   http.Header{},
	}
}

func searchRequest(body string) comparison.Request {
	return comparison.Request{
		Method: http.MethodPost,
		Host:   "weather.example:8080",
		Path:   "/v1/search",
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   []byte(body),
	}
}

func protobufRequest(payload []byte) comparison.Request {
	return comparison.Request{
		Method: http.MethodPost,
		Path:   "/test.v1.Service/Find",
		Header: http.Header{"Content-Type": []string{"application/x-protobuf"}},
		Body:   payload,
	}
}

func rpcRequest(method, body string) comparison.Request {
	return comparison.Request{
		Method: http.MethodPost,
		Host:   "rpc.example",
		Path:   "/test.v1.Service/" + method,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   []byte(body),
	}
}

func grpcRequest(t *testing.T, method string, payload []byte) comparison.Request {
	t.Helper()
	framed := grpcResponse(t, payload, true)
	return comparison.Request{
		Method: http.MethodPost,
		Host:   "rpc.example",
		Path:   "/test.v1.Service/" + method,
		Header: framed.Header,
		Body:   framed.Body,
	}
}

func withQuery(request comparison.Request, query string) comparison.Request {
	request.RawQuery = query
	return request
}

func withContentType(request comparison.Request, contentType string) comparison.Request {
	request.Header = http.Header{"Content-Type": []string{contentType}}
	return request
}

func withHost(request comparison.Request, host string) comparison.Request {
	request.Host = host
	return request
}

func queryProto(location string, days uint64) []byte {
	data := protowire.AppendTag(nil, 1, protowire.BytesType)
	data = protowire.AppendString(data, location)
	data = protowire.AppendTag(data, 2, protowire.VarintType)
	return protowire.AppendVarint(data, days)
}

func TestBindsRequiredScalarWithoutDescriptors(t *testing.T) {
	config := newSchemaConfig(t,
		map[string]string{"request.d.ts": `declare module "api" { interface Request { id: string; count?: number } }`},
		map[string]string{"request.ts": `
			import { egress } from "spectre";
			import type { Request } from "api";
			egress.match<Request>("http", "GET api.example/items/{id}");
		`})
	hasher, err := comparison.NewRequestHasher(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	assert.NoError(t, hasher.Configure(t.Context(), &descriptorpb.FileDescriptorSet{}))
	request := comparison.Request{Method: "GET", Host: "api.example", Path: "/items/one"}
	_, err = hasher.Hash(t.Context(), request)
	assert.NoError(t, err)
	request.RawQuery = "count=0"
	explicit, err := hasher.Hash(t.Context(), request)
	assert.NoError(t, err)
	request.RawQuery = ""
	absent, err := hasher.Hash(t.Context(), request)
	assert.NoError(t, err)
	assert.NotEqual(t, explicit, absent)
}

func TestBindsNumberLiteralsFromText(t *testing.T) {
	config := newSchemaConfig(t,
		map[string]string{"request.d.ts": `declare module "api" { interface Request { ratio: number | "NaN" } }`},
		map[string]string{"request.ts": `
			import { egress } from "spectre";
			import type { Request } from "api";
			egress.match<Request>("http", "GET api.example/items");
		`})
	hasher, err := comparison.NewRequestHasher(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	assert.NoError(t, hasher.Configure(t.Context(), &descriptorpb.FileDescriptorSet{}))
	request := comparison.Request{Method: "GET", Host: "api.example", Path: "/items", RawQuery: "ratio=NaN"}
	literal, err := hasher.Hash(t.Context(), request)
	assert.NoError(t, err)
	request.RawQuery = "ratio=1"
	number, err := hasher.Hash(t.Context(), request)
	assert.NoError(t, err)
	assert.NotEqual(t, literal, number)
	request.RawQuery = "ratio=Infinity"
	_, err = hasher.Hash(t.Context(), request)
	assert.Error(t, err)
}

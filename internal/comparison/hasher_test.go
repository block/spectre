package comparison_test

import (
	"bytes"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"testing"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/block/spectre/internal/comparison"
)

const egressScript = `
	spectre.egress("GET weather.example/v1/locations/{location}/forecast", "test.v1.Service.Find");
	spectre.egress("POST weather.example/v1/search", "test.v1.Service.Find");
	spectre.field("test.v1.Query.trace", () => undefined);
	spectre.field("test.v1.Query.tags", (tags) => tags === undefined ? undefined : tags.sort());
`

func TestHashesMethodAndCanonicalJSON(t *testing.T) {
	hasher := newHasher(t, egressScript)

	hash, err := hasher.Hash(t.Context(), searchRequest(`{"trace":"ignored","location":"london","days":3}`))

	assert.NoError(t, err)
	expected := sha256.Sum256([]byte("test.v1.Service.Find\x00" + `{"days":3,"location":"london"}`))
	assert.Equal(t, comparison.RequestHash(expected), hash)
}

func TestLogsRequestNormalisationWithoutNormalisers(t *testing.T) {
	var output bytes.Buffer
	config := comparison.NewConfig()
	config.ScriptsDir = writeScripts(t, map[string]string{"test.js": module(`
		spectre.egress("POST weather.example/v1/search", "test.v1.Service.Find");
	`)})
	log := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	hasher, err := comparison.NewRequestHasher(t.Context(), config, log)
	assert.NoError(t, err)
	assert.NoError(t, hasher.Configure(t.Context(), descriptorSet()))

	request := searchRequest(`{"location":"london"}`)
	request.Side = "candidate"

	_, err = hasher.Hash(t.Context(), request)

	assert.NoError(t, err)
	assert.Contains(t, output.String(), `"level":"DEBUG","msg":"Payload normalisation completed","message":"test.v1.Query","side":"candidate","normalisers":0`)
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
		"EnumNumber": {
			left:  forecastRequest("london", "units=1"),
			right: forecastRequest("london", "units=METRIC"),
			equal: true,
		},
		"PathQueryAndBody": {
			left:  forecastRequest("london", "days=3&tags=wind&filter.minDays=2"),
			right: searchRequest(`{"location":"london","days":3,"tags":["wind"],"filter":{"minDays":2}}`),
			equal: true,
		},
		"ExplicitDefault": {
			left:  forecastRequest("london", ""),
			right: forecastRequest("london", "days=0"),
		},
		"EscapedWildcard": {
			left:  forecastRequest("new%20york", ""),
			right: searchRequest(`{"location":"new york"}`),
			equal: true,
		},
		"ConnectAndGRPC": {
			left:  rpcRequest("Find", `{"location":"london","days":3}`),
			right: grpcRequest(t, "Find", queryProto("london", 3)),
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
		"UnknownQueryParameter": {request: forecastRequest("london", "unknown=1"), message: `bind query parameter "unknown": message "test.v1.Query" has no field "unknown"`},
		"UnknownBodyField":      {request: searchRequest(`{"unknown":1}`), message: "unknown field"},
		"InvalidValue":          {request: forecastRequest("london", "days=many"), message: `parse field "test.v1.Query.days"`},
		"UnknownEnumValue":      {request: forecastRequest("london", "units=KELVIN"), message: `enum "test.v1.Units" has no value "KELVIN"`},
		"RepeatedSingular":      {request: forecastRequest("london", "days=1&days=2"), message: `field "test.v1.Query.days" is not repeated`},
		"MessageParameter":      {request: forecastRequest("london", "filter=1"), message: `field "test.v1.Query.filter" is not a scalar or enum`},
		"PathAndQuery":          {request: forecastRequest("london", "location=paris"), message: `field "test.v1.Query.location" is already bound`},
		"BodyAndQuery":          {request: withQuery(searchRequest(`{"days":3}`), "days=3"), message: `field "test.v1.Query.days" is already bound`},
		"TwoNamesForOneField":   {request: forecastRequest("london", "filter.minDays=2&filter.min_days=2"), message: "is already bound"},
		"OneofMember":           {request: withQuery(searchRequest(`{"city":{"name":"london"}}`), "code=LON"), message: `field "test.v1.Query.code" is already bound`},
		"OneofParent":           {request: withQuery(searchRequest(`{"code":"LON"}`), "city.name=london"), message: `field "test.v1.Query.city" conflicts with a bound oneof member`},
		"GetWithBody":           {request: getWithBody, message: "GET request has a body"},
		"BodyNotJSON":           {request: withContentType(searchRequest(`{}`), "text/plain"), message: `body is not JSON: "text/plain"`},
		"UnknownHost":           {request: withHost(forecastRequest("london", ""), "other.example"), message: "request content type is not supported"},
		"UnknownMethod":         {request: rpcRequest("Missing", `{}`), message: `resolve method "test.v1.Service.Missing"`},
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
	config := comparison.NewConfig()
	config.ScriptsDir = writeScripts(t, map[string]string{"test.js": module(egressScript)})
	hasher, err := comparison.NewRequestHasher(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)

	_, err = hasher.Hash(t.Context(), searchRequest(`{}`))

	assert.EqualError(t, err, "comparison schema is not ready")
}

func TestRejectsUnbindableEndpoints(t *testing.T) {
	for name, test := range map[string]struct {
		pattern string
		method  string
		message string
	}{
		"UnknownField":     {pattern: "GET weather.example/v1/{place}", method: "Find", message: `message "test.v1.Query" has no field "place"`},
		"Repeated":         {pattern: "GET weather.example/v1/{tags}", method: "Find", message: `field "test.v1.Query.tags" is repeated`},
		"Message":          {pattern: "GET weather.example/v1/{filter}", method: "Find", message: `field "test.v1.Query.filter" is not a scalar or enum`},
		"ImplicitPresence": {pattern: "POST weather.example/v1/users", method: "Rename", message: `field "test.v1.User.name" must be optional`},
	} {
		t.Run(name, func(t *testing.T) {
			config := comparison.NewConfig()
			config.ScriptsDir = writeScripts(t, map[string]string{
				"weather.js": module(`spectre.egress("` + test.pattern + `", "test.v1.Service.` + test.method + `");`),
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
	// An ingress method absent from the schema would fail Configure if egress used it.
	hasher := newHasher(t, `spectre.ingress("GET /v1/forecast", "test.v1.Service.Missing");`)

	_, err := hasher.Hash(t.Context(), rpcRequest("Find", `{}`))

	assert.NoError(t, err)
}

func newHasher(t *testing.T, script string) *comparison.RequestHasher {
	t.Helper()
	config := comparison.NewConfig()
	config.ScriptsDir = writeScripts(t, map[string]string{"test.js": module(script)})
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

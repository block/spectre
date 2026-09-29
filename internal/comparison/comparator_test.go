package comparison_test

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	"github.com/grafana/sobek"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison"
)

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := map[string]func(*comparison.Config){
		"ScriptsDir": func(config *comparison.Config) { config.ScriptsDir = "" },
		"Timeout":    func(config *comparison.Config) { config.ComparisonTimeout = 0 },
		"BodyLimit":  func(config *comparison.Config) { config.ComparisonMaxResponseBytes = 0 },
	}
	for name, update := range tests {
		t.Run(name, func(t *testing.T) {
			config := comparison.NewConfig()
			config.ScriptsDir = writeScripts(t, map[string]string{"test.js": module("")})
			update(&config)
			comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
			assert.Error(t, err)
			assert.Equal(t, (*comparison.Comparator)(nil), comparator)
		})
	}
}

func TestRequiresLogger(t *testing.T) {
	config := comparison.NewConfig()
	config.ScriptsDir = writeScripts(t, map[string]string{"test.js": module("")})
	comparator, err := comparison.New(t.Context(), config, nil)

	assert.Error(t, err)
	assert.Equal(t, (*comparison.Comparator)(nil), comparator)
}

func TestRequiresSpectreModuleImport(t *testing.T) {
	config := comparison.NewConfig()
	config.ScriptsDir = writeScripts(t, map[string]string{"test.js": `spectre.field("test.v1.Response.ignored", () => true);`})
	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.Error(t, err)
	assert.Equal(t, (*comparison.Comparator)(nil), comparator)
}

func TestRejectsUnsupportedModuleImport(t *testing.T) {
	config := comparison.NewConfig()
	config.ScriptsDir = writeScripts(t, map[string]string{"test.js": `import "unsupported";`})

	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))

	assert.Error(t, err)
	assert.Equal(t, (*comparison.Comparator)(nil), comparator)
}

func TestPublicSpectreModuleStub(t *testing.T) {
	source, err := os.ReadFile("../../spectre.js")
	assert.NoError(t, err)
	module, err := sobek.ParseModule("spectre", string(source), nil)
	assert.NoError(t, err)
	var exports []string
	complete := module.GetExportedNames(func(names []string) { exports = names })

	assert.True(t, complete)
	assert.Equal(t, []string{"endpoint", "field", "message"}, exports)
}

func TestResultfFormatsReason(t *testing.T) {
	result := comparison.Resultf(comparison.Unable, "decode %s: %v", "status", errors.New("missing"))

	assert.Equal(t, comparison.Unable, result.Outcome())
	assert.Equal(t, "decode status: missing", result.Reason())
}

func TestDifferenceResultOwnsPaths(t *testing.T) {
	paths := []string{"$.name"}
	result := comparison.NewDifferenceResult(paths...)
	paths[0] = "changed"
	differences := result.Differences()
	differences[0] = "also changed"

	assert.Equal(t, []string{"$.name"}, result.Differences())
}

func TestConnectNormalisersIgnoreAndSortFields(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.ignored", () => undefined);
		spectre.field("test.v1.Response.roles", (roles) => roles.sort());
	`)
	reference := connectResponse(`{"stable":"same","ignored":"first","roles":["reader","writer"]}`)
	candidate := connectResponse(`{"roles":["writer","reader"],"ignored":"second","stable":"same"}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestPreservesProtoJSONInt64AsJavaScriptString(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.count", (count) => typeof count);
	`)
	reference := connectResponse(`{"count":"9007199254740993"}`)
	candidate := connectResponse(`{"count":"9007199254740994"}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestPassesUndefinedForMissingField(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.ignored", (value) => value === undefined ? "present" : value);
	`)
	reference := connectResponse(`{"stable":"same"}`)
	candidate := connectResponse(`{"stable":"same","ignored":"present"}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestReportsDifferencesRemainingAfterNormalisation(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.ignored", (value) => value.toLowerCase());
	`)
	reference := connectResponse(`{"stable":"same","ignored":"First"}`)
	candidate := connectResponse(`{"stable":"same","ignored":"second"}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.NewDifferenceResult("$.ignored"), result)
}

func TestParentNormaliserReceivesNormalisedChildren(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.ignored", (value) => value.toLowerCase());
		spectre.message("test.v1.Response", (response) => ({ignored: response.ignored}));
	`)
	reference := connectResponse(`{"stable":"first","ignored":"SAME"}`)
	candidate := connectResponse(`{"stable":"second","ignored":"same"}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestMessageNormaliserCanRemoveRoot(t *testing.T) {
	comparator := newComparator(t, `
		spectre.message("test.v1.Response", () => undefined);
	`)
	reference := connectResponse(`{"stable":"first"}`)
	candidate := connectResponse(`{"stable":"second"}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestFieldNormaliserReplacesMessageNormaliserAtSameLocation(t *testing.T) {
	comparator := newComparator(t, `
		spectre.message("test.v1.User", () => { throw new Error("message normaliser ran"); });
		spectre.field("test.v1.Response.owner", (owner) => owner === undefined ? undefined : owner.name.toLowerCase());
	`)
	reference := connectResponse(`{"owner":{"name":"ALICE"}}`)
	candidate := connectResponse(`{"owner":{"name":"alice"}}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestMessageNormaliserReceivesUndefinedForMissingMessage(t *testing.T) {
	comparator := newComparator(t, `
		spectre.message("test.v1.User", () => "ignored");
	`)
	reference := connectResponse(`{"stable":"same","owner":{"name":"alice"}}`)
	candidate := connectResponse(`{"stable":"same"}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestLogsComparisonAndIndividualNormaliserResults(t *testing.T) {
	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	config := comparison.NewConfig()
	config.ScriptsDir = writeScripts(t, map[string]string{"test.js": module(`
		spectre.field("test.v1.Response.ignored", () => undefined);
		spectre.field("test.v1.Response.stable", (value) => value);
	`)})
	comparator, err := comparison.New(t.Context(), config, log)
	assert.NoError(t, err)
	assert.NoError(t, comparator.Configure(t.Context(), descriptorSet()))
	reference := connectResponse(`{"stable":"first","ignored":"first"}`)
	candidate := connectResponse(`{"stable":"second","ignored":"second"}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.NewDifferenceResult("$.stable"), result)
	logs := output.String()
	assert.Contains(t, logs, `"msg":"Response normaliser completed","kind":"field","target":"test.v1.Response.ignored","side":"reference","response_path":"$.ignored"`)
	assert.Contains(t, logs, `"msg":"Response normaliser completed","kind":"field","target":"test.v1.Response.stable","side":"candidate","response_path":"$.stable"`)
	assert.Contains(t, logs, `"level":"DEBUG","msg":"Response comparison completed","path":"/test.v1.Service/Get","outcome":"divergent","differences":["$.stable"]`)
}

func TestAppliesFieldNormaliserToRepeatedMessageElements(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.users[].name", (name) => name.toLowerCase());
	`)
	reference := connectResponse(`{"stable":"same","users":[{"name":"ALICE"},{"name":"BOB"}]}`)
	candidate := connectResponse(`{"stable":"same","users":[{"name":"alice"},{"name":"bob"}]}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestMessageNormaliserCanRemoveRepeatedElement(t *testing.T) {
	comparator := newComparator(t, `
		spectre.message("test.v1.User", (user) =>
			user === undefined || user.name === "ignored" ? undefined : {name: user.name.toLowerCase()});
	`)
	reference := connectResponse(`{"users":[{"name":"ALICE"},{"name":"ignored"},{"name":"BOB"}]}`)
	candidate := connectResponse(`{"users":[{"name":"alice"},{"name":"bob"}]}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestComparesBinaryGRPCThroughProtoJSON(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.ignored", () => true);
	`)
	referencePayload := responseProto("same", "first", []string{"reader"})
	candidatePayload := responseProto("same", "second", []string{"reader"})
	reference := grpcResponse(t, referencePayload, true)
	candidate := grpcResponse(t, candidatePayload, false)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/grpc",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestComparesBinaryGRPCWithScalarMap(t *testing.T) {
	comparator := newComparator(t, "")
	reference := grpcResponse(t, responseProtoWithLabel("reference"), false)
	candidate := grpcResponse(t, responseProtoWithLabel("candidate"), false)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/grpc",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.NewDifferenceResult("$.labels.team"), result)
}

func TestReportsBinaryGRPCStatusAndBodyDifferences(t *testing.T) {
	comparator := newComparator(t, "")
	reference := grpcResponse(t, responseProto("first", "", nil), false)
	candidate := grpcResponse(t, responseProto("second", "", nil), false)

	bodyResult := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/grpc+proto",
		reference,
		candidate,
	)
	assert.Equal(t, comparison.NewDifferenceResult("$.stable"), bodyResult)

	candidate.Header.Set("Grpc-Status", "7")
	statusResult := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/grpc",
		reference,
		candidate,
	)
	assert.Equal(t, comparison.NewDifferenceResult("$status"), statusResult)

	reference.Header.Set("Grpc-Status", "invalid")
	invalidStatusResult := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/grpc",
		reference,
		candidate,
	)
	assert.Equal(t, comparison.Unable, invalidStatusResult.Outcome())
	assert.Contains(t, invalidStatusResult.Reason(), "parse gRPC status")
	assert.Contains(t, invalidStatusResult.Reason(), "invalid syntax")
}

func TestDefersStreamingAndGRPCWeb(t *testing.T) {
	comparator := newComparator(t, "")
	response := grpcResponse(t, responseProto("same", "", nil), false)
	excludedResponse := response
	excludedResponse.Overflow = true
	grpcNamespace := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo",
		"application/grpc",
		excludedResponse,
		excludedResponse,
	)
	assert.Equal(t, comparison.Resultf(
		comparison.Skipped,
		"gRPC namespace is excluded from response comparison",
	), grpcNamespace)

	streaming := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Watch",
		"application/grpc",
		response,
		response,
	)
	assert.Equal(t, comparison.Skipped, streaming.Outcome())

	grpcWeb := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/grpc-web+proto",
		response,
		response,
	)
	assert.Equal(t, comparison.Skipped, grpcWeb.Outcome())
}

func TestRejectsInvalidNormaliserResultsAndTargets(t *testing.T) {
	comparator := newComparator(t, `
		spectre.message("test.v1.Response", () => () => true);
	`)
	response := connectResponse(`{"stable":"same"}`)
	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		response,
		response,
	)
	assert.Equal(t, comparison.Unable, result.Outcome())

	config := comparison.NewConfig()
	config.ScriptsDir = writeScripts(t, map[string]string{
		"test.js": module(`spectre.field("test.v1.Response.unknown", () => true);`),
	})
	invalid, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	err = invalid.Configure(t.Context(), descriptorSet())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `prepare comparison plan`)
	assert.Contains(t, err.Error(), "has no field")
}

func TestInterruptsRunawayNormaliser(t *testing.T) {
	config := comparison.NewConfig()
	config.ScriptsDir = writeScripts(t, map[string]string{
		"test.js": module(`spectre.message("test.v1.Response", () => { while (true) {} });`),
	})
	config.ComparisonTimeout = 10 * time.Millisecond
	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	assert.NoError(t, comparator.Configure(t.Context(), descriptorSet()))
	response := connectResponse(`{"stable":"same"}`)

	result := comparator.Compare(
		t.Context(),
		http.MethodPost,
		"/test.v1.Service/Get",
		"application/json",
		response,
		response,
	)

	assert.Equal(t, comparison.Unable, result.Outcome())
}

func TestComparesHTTPJSONAsMethodOutput(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{"weather.js": forecastScript(`
		spectre.field("test.v1.Response.ignored", () => undefined);
		spectre.field("test.v1.Response.roles", (roles) => roles === undefined ? undefined : roles.sort());
	`)})
	for name, test := range map[string]struct {
		reference comparison.Response
		candidate comparison.Response
		expected  comparison.Result
	}{
		"Normalised": {
			reference: httpJSONResponse(http.StatusOK, "application/json; charset=utf-8", `{"stable":"same","ignored":"first","roles":["a","b"]}`),
			candidate: httpJSONResponse(http.StatusOK, "application/json", `{"roles":["b","a"],"ignored":"second","stable":"same"}`),
			expected:  comparison.Resultf(comparison.Equivalent, ""),
		},
		"ErrorStatusUsesMethodOutput": {
			reference: httpJSONResponse(http.StatusUnauthorized, "application/json", `{"stable":"denied","ignored":"first"}`),
			candidate: httpJSONResponse(http.StatusUnauthorized, "application/json", `{"stable":"denied","ignored":"second"}`),
			expected:  comparison.Resultf(comparison.Equivalent, ""),
		},
		"FieldDifference": {
			reference: httpJSONResponse(http.StatusOK, "application/json", `{"stable":"first"}`),
			candidate: httpJSONResponse(http.StatusOK, "application/json", `{"stable":"second"}`),
			expected:  comparison.NewDifferenceResult("$.stable"),
		},
		"StatusDifference": {
			reference: httpJSONResponse(http.StatusOK, "application/json", `{}`),
			candidate: httpJSONResponse(http.StatusForbidden, "application/json", `{}`),
			expected:  comparison.NewDifferenceResult("$status"),
		},
		"EmptyBodies": {
			reference: httpJSONResponse(http.StatusNoContent, "", ""),
			candidate: httpJSONResponse(http.StatusNoContent, "", ""),
			expected:  comparison.Resultf(comparison.Equivalent, ""),
		},
		"OneEmptyBody": {
			reference: httpJSONResponse(http.StatusOK, "application/json", `{}`),
			candidate: httpJSONResponse(http.StatusOK, "", ""),
			expected:  comparison.NewDifferenceResult("$"),
		},
		"NotJSON": {
			reference: httpJSONResponse(http.StatusInternalServerError, "text/html", `<html></html>`),
			candidate: httpJSONResponse(http.StatusInternalServerError, "text/html", `<html></html>`),
			expected:  comparison.Resultf(comparison.Unable, `HTTP response is not JSON: reference="text/html" candidate="text/html"`),
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := comparator.Compare(t.Context(), http.MethodGet, "/v1/forecast", "", test.reference, test.candidate)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestRejectsHTTPJSONWithUnknownFields(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{"weather.js": forecastScript("")})
	reference := httpJSONResponse(http.StatusOK, "application/json", `{"stable":"same"}`)
	candidate := httpJSONResponse(http.StatusOK, "application/json", `{"stable":"same","added":true}`)

	result := comparator.Compare(t.Context(), http.MethodGet, "/v1/forecast", "", reference, candidate)

	assert.Equal(t, comparison.Unable, result.Outcome())
	assert.Contains(t, result.Reason(), "candidate response does not match test.v1.Response")
	assert.Contains(t, result.Reason(), "unknown field")
}

func TestUndeclaredRequestsFallBackToRPCPaths(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{
		"weather.js": forecastScript(`spectre.field("test.v1.Response.ignored", () => undefined);`),
	})
	reference := httpJSONResponse(http.StatusOK, "application/json", `{"stable":"same","ignored":"first"}`)
	candidate := httpJSONResponse(http.StatusOK, "application/json", `{"stable":"same","ignored":"second"}`)

	rpc := comparator.Compare(t.Context(), http.MethodPost, "/test.v1.Service/Get", "application/json", reference, candidate)
	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), rpc)

	// Only the declared method matches, so HEAD is not compared as GET.
	for _, method := range []string{http.MethodPost, http.MethodHead} {
		undeclared := comparator.Compare(t.Context(), method, "/v1/forecast", "", reference, candidate)
		assert.Equal(t, comparison.Unable, undeclared.Outcome())
		assert.Contains(t, undeclared.Reason(), `RPC method is absent from the comparison schema: resolve method "v1.forecast"`)
	}
}

func TestComparesDeclaredHeadEndpoints(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{
		"weather.js": forecastScript(`spectre.endpoint("HEAD /v1/forecast", "test.v1.Service.Get");`),
	})
	response := httpJSONResponse(http.StatusOK, "application/json", "")

	result := comparator.Compare(t.Context(), http.MethodHead, "/v1/forecast", "", response, response)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestDeclaredEndpointsDecodeGRPCRequestsAsGRPC(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{
		"weather.js": module(`spectre.endpoint("POST /v1/forecast", "test.v1.Service.Get");`),
	})
	reference := grpcResponse(t, responseProto("first", "", nil), false)
	candidate := grpcResponse(t, responseProto("second", "", nil), false)

	result := comparator.Compare(t.Context(), http.MethodPost, "/v1/forecast", "application/grpc", reference, candidate)

	assert.Equal(t, comparison.NewDifferenceResult("$.stable"), result)
}

func TestLoadsEveryScriptAsOneSet(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{
		"users/user.js": module(`
			import {lower} from "../helpers/strings.js";
			spectre.message("test.v1.User", (user) => user === undefined ? undefined : {name: lower(user.name)});
		`),
		"weather.js": module(`
			import {lower} from "./helpers/strings.js";
			spectre.endpoint("GET /v1/forecast", "test.v1.Service.Get");
			spectre.field("test.v1.Response.stable", (stable) => stable === undefined ? undefined : lower(stable));
		`),
		"helpers/strings.js": `export const lower = (value) => value.toLowerCase();`,
		"README.md":          "Not a script.",
	})
	reference := connectResponse(`{"stable":"SAME","owner":{"name":"ALICE"}}`)
	candidate := connectResponse(`{"stable":"same","owner":{"name":"alice"}}`)

	for name, test := range map[string]struct {
		method string
		path   string
	}{
		"RPC":  {http.MethodPost, "/test.v1.Service/Get"},
		"HTTP": {http.MethodGet, "/v1/forecast"},
	} {
		t.Run(name, func(t *testing.T) {
			result := comparator.Compare(t.Context(), test.method, test.path, "application/json", reference, candidate)
			assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
		})
	}
}

func TestComparesRPCsWithoutScripts(t *testing.T) {
	comparator := newScriptsComparator(t, nil)
	reference := connectResponse(`{"stable":"first"}`)
	candidate := connectResponse(`{"stable":"second"}`)

	result := comparator.Compare(t.Context(), http.MethodPost, "/test.v1.Service/Get", "application/json", reference, candidate)

	assert.Equal(t, comparison.NewDifferenceResult("$.stable"), result)
}

func TestRejectsInvalidEndpointDeclarations(t *testing.T) {
	for name, test := range map[string]struct {
		scripts map[string]string
		message string
	}{
		"Host": {
			scripts: map[string]string{"weather.js": module(`spectre.endpoint("GET weather.example/v1/forecast", "test.v1.Service.Get");`)},
			message: "must not include a host",
		},
		"NoMethod": {
			scripts: map[string]string{"weather.js": module(`spectre.endpoint("/v1/forecast", "test.v1.Service.Get");`)},
			message: `must have the form "<METHOD> /<path>"`,
		},
		"InvalidMethodName": {
			scripts: map[string]string{"weather.js": module(`spectre.endpoint("GET /v1/forecast", "not a name");`)},
			message: `endpoint "GET /v1/forecast" has an invalid method name "not a name"`,
		},
		"Conflict": {
			scripts: map[string]string{
				"a.js": module(`spectre.endpoint("GET /v1/{location}/forecast", "test.v1.Service.Get");`),
				"b.js": module(`spectre.endpoint("GET /v1/units/{fee}", "test.v1.Service.Get");`),
			},
			message: `route endpoint "GET /v1/units/{fee}": invalid route pattern`,
		},
		"DuplicateAcrossScripts": {
			scripts: map[string]string{
				"a.js": module(`spectre.endpoint("GET /v1/forecast", "test.v1.Service.Get");`),
				"b.js": module(`spectre.endpoint("GET /v1/forecast", "test.v1.Service.Get");`),
			},
			message: `duplicate endpoint "GET /v1/forecast"`,
		},
		"DuplicateTargetAcrossScripts": {
			scripts: map[string]string{
				"a.js": module(`spectre.message("test.v1.User", (user) => user);`),
				"b.js": module(`spectre.message("test.v1.User", (user) => user);`),
			},
			message: `duplicate normaliser target "test.v1.User"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := comparison.NewConfig()
			config.ScriptsDir = writeScripts(t, test.scripts)
			_, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestRejectsEndpointsAbsentFromSchema(t *testing.T) {
	for name, test := range map[string]struct {
		method  string
		message string
	}{
		"Streaming": {method: "test.v1.Service.Watch", message: `endpoint method "test.v1.Service.Watch" must be unary`},
		"Unknown":   {method: "test.v1.Service.Missing", message: `resolve method "test.v1.Service.Missing"`},
		"Message":   {method: "test.v1.Response", message: "is not a method"},
	} {
		t.Run(name, func(t *testing.T) {
			config := comparison.NewConfig()
			config.ScriptsDir = writeScripts(t, map[string]string{
				"weather.js": module(`spectre.endpoint("GET /v1/forecast", "` + test.method + `");`),
			})
			comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
			assert.NoError(t, err)
			err = comparator.Configure(t.Context(), descriptorSet())
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `resolve endpoint "GET /v1/forecast"`)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func newScriptsComparator(t *testing.T, scripts map[string]string) *comparison.Comparator {
	t.Helper()
	config := comparison.NewConfig()
	config.ScriptsDir = writeScripts(t, scripts)
	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	assert.NoError(t, comparator.Configure(t.Context(), descriptorSet()))
	return comparator
}

func httpJSONResponse(status int, contentType, body string) comparison.Response {
	return comparison.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       []byte(body),
	}
}

func newComparator(t *testing.T, script string) *comparison.Comparator {
	t.Helper()
	return newScriptsComparator(t, map[string]string{"test.js": module(script)})
}

// forecastScript declares GET /v1/forecast as a raw HTTP endpoint typed by test.v1.Service.Get.
func forecastScript(body string) string {
	return module(`spectre.endpoint("GET /v1/forecast", "test.v1.Service.Get");` + body)
}

func module(body string) string {
	return `import * as spectre from "spectre";` + body
}

func writeScripts(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, source := range scripts {
		path := filepath.Join(dir, filepath.FromSlash(name))
		assert.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		assert.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	}
	return dir
}

func connectResponse(body string) comparison.Response {
	return comparison.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(body),
	}
}

func grpcResponse(t *testing.T, payload []byte, compressed bool) comparison.Response {
	t.Helper()
	flag := byte(0)
	if compressed {
		flag = 1
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		_, err := writer.Write(payload)
		assert.NoError(t, err)
		assert.NoError(t, writer.Close())
		payload = buffer.Bytes()
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
	return comparison.Response{StatusCode: http.StatusOK, Header: header, Body: body}
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
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("Request")},
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
				Name: new("Response"),
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
						Name:   new("ignored"),
						Number: proto.Int32(2),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
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
					Name:            new("Watch"),
					InputType:       new(".test.v1.Request"),
					OutputType:      new(".test.v1.Response"),
					ServerStreaming: new(true),
				},
			},
		}},
	}}}
}

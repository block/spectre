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
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/descriptors"
)

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := map[string]func(*comparison.Config){
		"ScriptsDir": func(config *comparison.Config) { config.ScriptsDir = "" },
		"SchemaDir":  func(config *comparison.Config) { config.Schema.SchemaDir = "" },
		"Timeout":    func(config *comparison.Config) { config.ComparisonTimeout = 0 },
		"BodyLimit":  func(config *comparison.Config) { config.ComparisonMaxBodyBytes = 0 },
	}
	for name, update := range tests {
		t.Run(name, func(t *testing.T) {
			config := newConfig(t, map[string]string{"test.ts": module("")})
			update(&config)
			comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
			assert.Error(t, err)
			assert.Equal(t, (*comparison.Comparator)(nil), comparator)
		})
	}
}

func TestRequiresLogger(t *testing.T) {
	config := newConfig(t, map[string]string{"test.ts": module("")})
	comparator, err := comparison.New(t.Context(), config, nil)

	assert.Error(t, err)
	assert.Equal(t, (*comparison.Comparator)(nil), comparator)
}

func TestRequiresSpectreModuleImport(t *testing.T) {
	config := newConfig(t, map[string]string{"test.ts": `
		import type * as v1 from "test.v1";
		spectre.field<v1.Response, "ignored">(() => undefined);
	`})
	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Cannot find name 'spectre'")
	assert.Equal(t, (*comparison.Comparator)(nil), comparator)
}

func TestRejectsUnsupportedModuleImport(t *testing.T) {
	config := newConfig(t, map[string]string{"test.ts": `import "unsupported";`})

	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))

	assert.Error(t, err)
	assert.Equal(t, (*comparison.Comparator)(nil), comparator)
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
		spectre.field<v1.Response, "ignored">(() => undefined);
		spectre.field<v1.Response, "roles">((roles) => roles.sort());
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
		spectre.field<v1.Response, "count">((count) => typeof count);
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
		spectre.field<v1.Response, "ignored">((value) => value === undefined ? "present" : value);
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

func TestWireProtocolsEmitDefaultValues(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field<v1.Response, "stable">((value) => {
			if (value !== "") {
				throw new Error("missing scalar did not receive its default");
			}
			return value;
		});
		spectre.message<v1.Response>((response) => {
			if (response === undefined || response.stable !== "" || response.count !== "0" ||
				response.roles.length !== 0 || response.users.length !== 0 ||
				Object.keys(response.labels).length !== 0 || response.ignored !== undefined) {
				throw new Error("unexpected ProtoJSON defaults");
			}
			return response;
		});
	`)
	for name, test := range map[string]struct {
		contentType string
		reference   comparison.Response
		candidate   comparison.Response
	}{
		"ConnectJSON": {
			contentType: "application/json",
			reference:   connectResponse(`{}`),
			candidate:   connectResponse(`{"stable":"","roles":[],"users":[],"count":"0","labels":{}}`),
		},
		"GRPC": {
			contentType: "application/grpc",
			reference:   grpcResponse(t, nil, false),
			candidate:   grpcResponse(t, responseProto("", "", nil), false),
		},
		"Protobuf": {
			contentType: "application/x-protobuf",
			reference:   protobufResponse(nil),
			candidate:   protobufResponse(responseProto("", "", nil)),
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := comparator.Compare(t.Context(), http.MethodPost, "/test.v1.Service/Get", test.contentType, test.reference, test.candidate)
			assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
		})
	}
}

func TestReportsDifferencesRemainingAfterNormalisation(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field<v1.Response, "ignored">((value) => value?.toLowerCase());
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
		spectre.field<v1.Response, "ignored">((value) => value?.toLowerCase());
		// Normalisers may return any JSON, so the result need not be a Response.
		spectre.message<v1.Response>((response): any => ({ignored: response?.ignored}));
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
		spectre.message<v1.Response>(() => undefined);
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
		spectre.message<v1.User>(() => { throw new Error("message normaliser ran"); });
		spectre.field<v1.Response, "owner">((owner): any => owner === undefined ? undefined : owner.name.toLowerCase());
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
		spectre.message<v1.User>((): any => "ignored");
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
	config := newConfig(t, map[string]string{"test.ts": module(`
		spectre.field<v1.Response, "ignored">(() => undefined);
		spectre.field<v1.Response, "stable">((value) => value);
	`)})
	comparator, err := comparison.New(t.Context(), config, log)
	assert.NoError(t, err)
	set := descriptorSet()
	assert.NoError(t, comparator.Configure(t.Context(), set))
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
	assert.Contains(t, logs, `"msg":"Payload normaliser completed","kind":"field","target":"test.v1.Response.ignored","side":"reference","payload_path":"$.ignored"`)
	assert.Contains(t, logs, `"msg":"Payload normaliser completed","kind":"field","target":"test.v1.Response.stable","side":"candidate","payload_path":"$.stable"`)
	assert.Contains(t, logs, `"msg":"Payload normalisation completed","message":"test.v1.Response","side":"reference","normalisers":2`)
	assert.Contains(t, logs, `"level":"INFO","msg":"Response comparison completed","event":"correlation","path":"/test.v1.Service/Get","outcome":"divergent","differences":["$.stable"]`)
}

func TestAppliesFieldNormaliserToRepeatedMessageElements(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field<v1.Response, "users[].name">((name) => name.toLowerCase());
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
		spectre.message<v1.User>((user) =>
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
		spectre.field<v1.Response, "ignored">((): any => true);
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
		spectre.message<v1.Response>((): any => () => true);
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

	// The cast skips type checking, so the plan's own check rejects the path.
	config := newConfig(t, map[string]string{
		"test.ts": module(`(spectre.field as any)("test.v1.Response", "unknown", () => true);`),
	})
	invalid, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	set := descriptorSet()
	err = invalid.Configure(t.Context(), set)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `prepare comparison plan`)
	assert.Contains(t, err.Error(), "has no field")
}

func TestInterruptsRunawayNormaliser(t *testing.T) {
	config := newConfig(t, map[string]string{
		"test.ts": module(`spectre.message<v1.Response>(() => { while (true) {} });`),
	})
	config.ComparisonTimeout = 10 * time.Millisecond
	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	set := descriptorSet()
	assert.NoError(t, comparator.Configure(t.Context(), set))
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

func TestComparesHTTPJSONAsDeclaredType(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{"weather.ts": forecastScript(`
		spectre.field<v1.Response, "ignored">(() => undefined);
		spectre.field<v1.Response, "roles">((roles) => roles === undefined ? undefined : roles.sort());
	`)})
	for name, test := range map[string]struct {
		reference comparison.Response
		candidate comparison.Response
		expected  comparison.Result
	}{
		"Normalised": {
			reference: httpJSONResponse(http.StatusOK, "application/json; charset=utf-8", `{"stable":"same","ignored":"first","roles":["a","b"],"users":[],"count":"0","labels":{}}`),
			candidate: httpJSONResponse(http.StatusOK, "application/json", `{"roles":["b","a"],"ignored":"second","stable":"same","users":[],"count":"0","labels":{}}`),
			expected:  comparison.Resultf(comparison.Equivalent, ""),
		},
		"ErrorStatusUsesDeclaredType": {
			reference: httpJSONResponse(http.StatusUnauthorized, "application/json", `{"stable":"denied","ignored":"first","roles":[],"users":[],"count":"0","labels":{}}`),
			candidate: httpJSONResponse(http.StatusUnauthorized, "application/json", `{"stable":"denied","ignored":"second","roles":[],"users":[],"count":"0","labels":{}}`),
			expected:  comparison.Resultf(comparison.Equivalent, ""),
		},
		"FieldDifference": {
			reference: httpJSONResponse(http.StatusOK, "application/json", `{"stable":"first","roles":[],"users":[],"count":"0","labels":{}}`),
			candidate: httpJSONResponse(http.StatusOK, "application/json", `{"stable":"second","roles":[],"users":[],"count":"0","labels":{}}`),
			expected:  comparison.NewDifferenceResult("$.stable"),
		},
		"StatusDifference": {
			reference: httpJSONResponse(http.StatusOK, "application/json", `{"stable":"","roles":[],"users":[],"count":"0","labels":{}}`),
			candidate: httpJSONResponse(http.StatusForbidden, "application/json", `{"stable":"","roles":[],"users":[],"count":"0","labels":{}}`),
			expected:  comparison.NewDifferenceResult("$status"),
		},
		"EmptyBodies": {
			reference: httpJSONResponse(http.StatusNoContent, "", ""),
			candidate: httpJSONResponse(http.StatusNoContent, "", ""),
			expected:  comparison.Resultf(comparison.Equivalent, ""),
		},
		"OneEmptyBody": {
			reference: httpJSONResponse(http.StatusOK, "application/json", `{"stable":"","roles":[],"users":[],"count":"0","labels":{}}`),
			candidate: httpJSONResponse(http.StatusOK, "", ""),
			expected:  comparison.Resultf(comparison.Unable, `cannot decode candidate response as test.v1.Response: validate response: $: required field "stable" of type "test.v1.Response" is missing`),
		},
		"NotJSON": {
			reference: httpJSONResponse(http.StatusInternalServerError, "text/html", `<html></html>`),
			candidate: httpJSONResponse(http.StatusInternalServerError, "text/html", `<html></html>`),
			expected:  comparison.Resultf(comparison.Unable, `cannot decode reference response as test.v1.Response: body is not JSON: "text/html"`),
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := comparator.Compare(t.Context(), http.MethodGet, "/v1/forecast", "", test.reference, test.candidate)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestRejectsHTTPJSONWithUnknownFields(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{"weather.ts": forecastScript("")})
	reference := httpJSONResponse(http.StatusOK, "application/json", `{"stable":"same","roles":[],"users":[],"count":"0","labels":{}}`)
	candidate := httpJSONResponse(http.StatusOK, "application/json", `{"stable":"same","roles":[],"users":[],"count":"0","labels":{},"added":true}`)

	result := comparator.Compare(t.Context(), http.MethodGet, "/v1/forecast", "", reference, candidate)

	assert.Equal(t, comparison.Unable, result.Outcome())
	assert.Contains(t, result.Reason(), "cannot decode candidate response as test.v1.Response")
	assert.Contains(t, result.Reason(), `type "test.v1.Response" has no field "added"`)
}

func TestComparesHTTPJSONWithoutDescriptors(t *testing.T) {
	declarations := map[string]string{"response.d.ts": `
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
		}
	`}
	for name, test := range map[string]struct {
		script    string
		reference string
		candidate string
		expected  comparison.Result
		message   string
	}{
		"OptionalFieldsMayBeAbsent": {
			reference: `{"stable":"same"}`,
			candidate: `{"stable":"same"}`,
			expected:  comparison.Resultf(comparison.Equivalent, ""),
		},
		"Normalised": {
			script:    `spectre.field<raw.Response, "ignored">(() => undefined);`,
			reference: `{"stable":"same","ignored":"first"}`,
			candidate: `{"ignored":"second","stable":"same"}`,
			expected:  comparison.Resultf(comparison.Equivalent, ""),
		},
		"ExplicitDefaultsRemainPresent": {
			reference: `{"stable":"same"}`,
			candidate: `{"stable":"same","ignored":"","count":0,"enabled":false,"roles":[],"metadata":{}}`,
			expected:  comparison.NewDifferenceResult("$.count", "$.enabled", "$.ignored", "$.metadata", "$.roles"),
		},
		"UnknownField": {
			reference: `{"stable":"same"}`,
			candidate: `{"stable":"same","added":true}`,
			message:   `type "raw.Response" has no field "added"`,
		},
		"RequiredField": {
			reference: `{"stable":"same"}`,
			candidate: `{}`,
			message:   `required field "stable" of type "raw.Response" is missing`,
		},
		"NullScalar": {
			reference: `{"stable":"same"}`,
			candidate: `{"stable":"same","ignored":null}`,
			message:   "$.ignored: expected string, found null",
		},
		"NullMessage": {
			reference: `{"stable":"same"}`,
			candidate: `{"stable":"same","owner":null}`,
			message:   "$.owner: expected raw.User, found null",
		},
		"NullRoot": {
			reference: `{"stable":"same"}`,
			candidate: `null`,
			message:   "$: expected raw.Response, found null",
		},
		"NoImplicitScalarConversion": {
			reference: `{"stable":"same","count":0}`,
			candidate: `{"stable":"same","count":"0"}`,
			message:   "$.count: expected number, found a string",
		},
		"FieldNormaliserMayChangeType": {
			script:    `spectre.field<raw.Response, "stable">((): any => ({replacement: true}));`,
			reference: `{"stable":"first"}`,
			candidate: `{"stable":"second"}`,
			expected:  comparison.Resultf(comparison.Equivalent, ""),
		},
		"MessageNormaliserMayChangeType": {
			script:    `spectre.message<raw.Response>((): any => [true, null, {replacement: "value"}]);`,
			reference: `{"stable":"first"}`,
			candidate: `{"stable":"second"}`,
			expected:  comparison.Resultf(comparison.Equivalent, ""),
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := newSchemaConfig(t, declarations, map[string]string{
				"raw.ts": `import * as spectre from "spectre"; import type * as raw from "raw";` +
					`spectre.ingress<raw.Response>("http", "GET /raw");` + test.script,
			})
			comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
			assert.NoError(t, err)
			assert.NoError(t, comparator.Configure(t.Context(), &descriptorpb.FileDescriptorSet{}))
			reference := httpJSONResponse(http.StatusOK, "application/json", test.reference)
			candidate := httpJSONResponse(http.StatusOK, "application/json", test.candidate)

			result := comparator.Compare(t.Context(), http.MethodGet, "/raw", "", reference, candidate)

			if test.message != "" {
				assert.Equal(t, comparison.Unable, result.Outcome())
				assert.Contains(t, result.Reason(), "cannot decode candidate response as raw.Response")
				assert.Contains(t, result.Reason(), test.message)
				return
			}
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestUndeclaredRequestsFallBackToRPCPaths(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{
		"weather.ts": forecastScript(`spectre.field<v1.Response, "ignored">(() => undefined);`),
	})
	reference := httpJSONResponse(http.StatusOK, "application/json", `{"stable":"same","ignored":"first"}`)
	candidate := httpJSONResponse(http.StatusOK, "application/json", `{"stable":"same","ignored":"second"}`)

	rpc := comparator.Compare(t.Context(), http.MethodPost, "/test.v1.Service/Get", "application/json", reference, candidate)
	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), rpc)

	// Only the declared method matches, so HEAD is not compared as GET.
	for _, method := range []string{http.MethodPost, http.MethodHead} {
		undeclared := comparator.Compare(t.Context(), method, "/v1/forecast", "", reference, candidate)
		assert.Equal(t, comparison.Unable, undeclared.Outcome())
		assert.Contains(t, undeclared.Reason(), `RPC method is absent from the comparison schema: operation "v1.forecast" is not declared`)
	}
}

func TestComparesBodylessHTTPResponses(t *testing.T) {
	for name, test := range map[string]struct {
		method string
		status int
	}{
		"Head":        {method: http.MethodHead, status: http.StatusOK},
		"NoContent":   {method: http.MethodGet, status: http.StatusNoContent},
		"NotModified": {method: http.MethodGet, status: http.StatusNotModified},
	} {
		t.Run(name, func(t *testing.T) {
			config := newSchemaConfig(t,
				map[string]string{"response.d.ts": `declare module "raw" { interface Response { stable: string } }`},
				map[string]string{"raw.ts": `
					import * as spectre from "spectre";
					import type { Response } from "raw";
					spectre.ingress<Response>("http", "` + test.method + ` /raw");
					spectre.message<Response>(() => { throw new Error("normaliser ran"); });
				`})
			comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
			assert.NoError(t, err)
			assert.NoError(t, comparator.Configure(t.Context(), &descriptorpb.FileDescriptorSet{}))
			response := httpJSONResponse(test.status, "", "")

			result := comparator.Compare(t.Context(), test.method, "/raw", "", response, response)

			assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
		})
	}
}

func TestValidatesEmptyHTTPResponses(t *testing.T) {
	for name, test := range map[string]struct {
		field     string
		script    string
		reference string
		candidate string
		message   string
	}{
		"RequiredBothEmpty":      {field: "stable: string", message: `cannot decode reference response as raw.Response: validate response: $: required field "stable" of type "raw.Response" is missing`},
		"RequiredReferenceEmpty": {field: "stable: string", candidate: `{"stable":"same"}`, message: `cannot decode reference response as raw.Response: validate response: $: required field "stable" of type "raw.Response" is missing`},
		"RequiredCandidateEmpty": {field: "stable: string", reference: `{"stable":"same"}`, message: `cannot decode candidate response as raw.Response: validate response: $: required field "stable" of type "raw.Response" is missing`},
		"OptionalBothEmpty":      {field: "stable?: string"},
		"OptionalEmptyObject":    {field: "stable?: string", candidate: `{}`},
		"OptionalRunsNormaliser": {
			field:   "stable?: string",
			script:  `spectre.message<Response>(() => { throw new Error("normaliser ran"); });`,
			message: "normalise reference response",
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := newSchemaConfig(t,
				map[string]string{"response.d.ts": `declare module "raw" { interface Response { ` + test.field + ` } }`},
				map[string]string{"raw.ts": `
					import * as spectre from "spectre";
					import type { Response } from "raw";
					spectre.ingress<Response>("http", "GET /raw");
				` + test.script})
			comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
			assert.NoError(t, err)
			assert.NoError(t, comparator.Configure(t.Context(), &descriptorpb.FileDescriptorSet{}))
			reference := httpJSONResponse(http.StatusOK, "application/json", test.reference)
			candidate := httpJSONResponse(http.StatusOK, "application/json", test.candidate)

			result := comparator.Compare(t.Context(), http.MethodGet, "/raw", "", reference, candidate)

			if test.message != "" {
				assert.Equal(t, comparison.Unable, result.Outcome())
				assert.Contains(t, result.Reason(), test.message)
				return
			}
			assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
		})
	}
}

func TestIgnoresEgressEndpoints(t *testing.T) {
	// An undeclared egress type does not participate in ingress configuration. The
	// cast skips type checking, so the type reaches Configure.
	comparator := newScriptsComparator(t, map[string]string{
		"weather.ts": module(`(spectre.egress as any)("test.v1.Missing", "http", "GET weather.example/v1/forecast");`),
	})
	response := httpJSONResponse(http.StatusOK, "application/json", `{"stable":"same"}`)

	result := comparator.Compare(t.Context(), http.MethodGet, "/v1/forecast", "application/json", response, response)

	assert.Equal(t, comparison.Unable, result.Outcome())
	assert.Contains(t, result.Reason(), `operation "v1.forecast" is not declared`)
}

func TestDeclaredEndpointsDecodeGRPCRequestsAsGRPC(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{
		"weather.ts": module(`spectre.ingress<v1.Response>("http", "POST /v1/forecast");`),
	})
	reference := grpcResponse(t, responseProto("first", "", nil), false)
	candidate := grpcResponse(t, responseProto("second", "", nil), false)

	result := comparator.Compare(t.Context(), http.MethodPost, "/v1/forecast", "application/grpc", reference, candidate)

	assert.Equal(t, comparison.NewDifferenceResult("$.stable"), result)
}

func TestDeclaredEndpointsDecodeProtobufRequestsAsProtobuf(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{
		"weather.ts": module(`spectre.ingress<v1.Response>("http", "POST /v1/forecast");`),
	})
	reference := protobufResponse(responseProto("first", "", nil))
	candidate := protobufResponse(responseProto("second", "", nil))

	result := comparator.Compare(t.Context(), http.MethodPost, "/v1/forecast", "application/x-protobuf", reference, candidate)

	assert.Equal(t, comparison.NewDifferenceResult("$.stable"), result)
}

func TestDeclaredEndpointsDoNotRequireUnaryMethods(t *testing.T) {
	for name, path := range map[string]string{
		"StreamingMethod": "/test.v1.Service/Watch",
		"MissingMethod":   "/test.v1.Service/Missing",
	} {
		t.Run(name, func(t *testing.T) {
			comparator := newScriptsComparator(t, map[string]string{
				"test.ts": module(`spectre.ingress<v1.Response>("http", "POST ` + path + `");`),
			})
			reference := grpcResponse(t, responseProto("first", "", nil), false)
			candidate := grpcResponse(t, responseProto("second", "", nil), false)

			result := comparator.Compare(t.Context(), http.MethodPost, path, "application/grpc", reference, candidate)

			assert.Equal(t, comparison.NewDifferenceResult("$.stable"), result)
		})
	}
}

func TestLoadsEveryScriptAsOneSet(t *testing.T) {
	comparator := newScriptsComparator(t, map[string]string{
		"users/user.ts": module(`
			import {lower} from "../helpers/strings";
			spectre.message<v1.User>((user) => user === undefined ? undefined : {name: lower(user.name)});
		`),
		"weather.ts": module(`
			import {lower} from "./helpers/strings";
			spectre.ingress<v1.Response>("http", "GET /v1/forecast");
			spectre.field<v1.Response, "stable">((stable) => stable === undefined ? undefined : lower(stable));
		`),
		"helpers/strings.ts": `export const lower = (value: string) => value.toLowerCase();`,
		"README.md":          "Not a script.",
	})
	reference := connectResponse(`{"stable":"SAME","roles":[],"users":[],"count":"0","labels":{},"owner":{"name":"ALICE"}}`)
	candidate := connectResponse(`{"stable":"same","roles":[],"users":[],"count":"0","labels":{},"owner":{"name":"alice"}}`)

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
			scripts: map[string]string{"weather.ts": module(`spectre.ingress<v1.Response>("http", "GET weather.example/v1/forecast");`)},
			message: `must have the form "<METHOD> /<path>"`,
		},
		"NoMethod": {
			scripts: map[string]string{"weather.ts": module(`spectre.ingress<v1.Response>("http", "/v1/forecast");`)},
			message: `must have the form "<METHOD> /<path>"`,
		},
		"Conflict": {
			scripts: map[string]string{
				"a.ts": module(`spectre.ingress<v1.Response>("http", "GET /v1/{location}/forecast");`),
				"b.ts": module(`spectre.ingress<v1.Response>("http", "GET /v1/units/{fee}");`),
			},
			message: `route endpoint "GET /v1/units/{fee}": invalid route pattern`,
		},
		"DuplicateAcrossScripts": {
			scripts: map[string]string{
				"a.ts": module(`spectre.ingress<v1.Response>("http", "GET /v1/forecast");`),
				"b.ts": module(`spectre.ingress<v1.Response>("http", "GET /v1/forecast");`),
			},
			message: `duplicate endpoint "GET /v1/forecast"`,
		},
		"DuplicateTargetAcrossScripts": {
			scripts: map[string]string{
				"a.ts": module(`spectre.message<v1.User>((user) => user);`),
				"b.ts": module(`spectre.message<v1.User>((user) => user);`),
			},
			message: `duplicate normaliser target "test.v1.User"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := newConfig(t, test.scripts)
			_, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestRejectsEndpointsAbsentFromSchema(t *testing.T) {
	for name, typeName := range map[string]string{
		"Unknown":   "test.v1.Missing",
		"Operation": "test.v1.Service.Get",
		"NotAName":  "not a name",
	} {
		t.Run(name, func(t *testing.T) {
			config := newConfig(t, map[string]string{
				// The cast skips type checking, so the type reaches Configure.
				"weather.ts": module(`(spectre.ingress as any)("` + typeName + `", "http", "GET /v1/forecast");`),
			})
			comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
			assert.NoError(t, err)
			set := descriptorSet()
			err = comparator.Configure(t.Context(), set)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), `resolve endpoint "GET /v1/forecast"`)
			assert.Contains(t, err.Error(), `type "`+typeName+`" is not declared`)
		})
	}
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

// newConfig types scripts against testDeclarations.
func newConfig(t *testing.T, scripts map[string]string) comparison.Config {
	t.Helper()
	return newSchemaConfig(t, testDeclarations(t), scripts)
}

func newSchemaConfig(t *testing.T, declarations, scripts map[string]string) comparison.Config {
	t.Helper()
	config := comparison.NewConfig()
	config.Schema.SchemaDir = writeScripts(t, declarations)
	config.ScriptsDir = writeScripts(t, scripts)
	return config
}

func newScriptsComparator(t *testing.T, scripts map[string]string) *comparison.Comparator {
	t.Helper()
	config := newConfig(t, scripts)
	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	set := descriptorSet()
	assert.NoError(t, comparator.Configure(t.Context(), set))
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
	return newScriptsComparator(t, map[string]string{"test.ts": module(script)})
}

// forecastScript declares GET /v1/forecast as a raw HTTP endpoint typed by test.v1.Response.
func forecastScript(body string) string {
	return module(`spectre.ingress<v1.Response>("http", "GET /v1/forecast");` + body)
}

// module imports the spectre API and the test schema modules, so body can name their types.
func module(body string) string {
	return `import * as spectre from "spectre"; import type * as v1 from "test.v1"; import type * as weather from "weather";` + body
}

func writeScripts(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, source := range files {
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

func protobufResponse(payload []byte) comparison.Response {
	return comparison.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/x-protobuf"}},
		Body:       payload,
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
func withProto3Optional(message *descriptorpb.DescriptorProto) *descriptorpb.DescriptorProto {
	for _, field := range message.GetField() {
		if field.OneofIndex != nil || field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE ||
			field.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
			continue
		}
		field.Proto3Optional = new(true)
		field.OneofIndex = new(int32(len(message.GetOneofDecl())))
		message.OneofDecl = append(message.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: new("_" + field.GetName())})
	}
	return message
}

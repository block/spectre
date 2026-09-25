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
		"Script":    func(config *comparison.Config) { config.ComparisonScript = "" },
		"Timeout":   func(config *comparison.Config) { config.ComparisonTimeout = 0 },
		"BodyLimit": func(config *comparison.Config) { config.ComparisonMaxResponseBytes = 0 },
	}
	for name, update := range tests {
		t.Run(name, func(t *testing.T) {
			config := comparison.NewConfig()
			config.ComparisonScript = writeScript(t, comparisonModule(""))
			update(&config)
			comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
			assert.Error(t, err)
			assert.Equal(t, (*comparison.Comparator)(nil), comparator)
		})
	}
}

func TestRequiresLogger(t *testing.T) {
	config := comparison.NewConfig()
	config.ComparisonScript = writeScript(t, comparisonModule(""))
	comparator, err := comparison.New(t.Context(), config, nil)

	assert.Error(t, err)
	assert.Equal(t, (*comparison.Comparator)(nil), comparator)
}

func TestRequiresSpectreModuleImport(t *testing.T) {
	config := comparison.NewConfig()
	config.ComparisonScript = writeScript(t, `spectre.field("test.v1.Response.ignored", () => true);`)
	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.Error(t, err)
	assert.Equal(t, (*comparison.Comparator)(nil), comparator)
}

func TestRejectsUnsupportedModuleImport(t *testing.T) {
	config := comparison.NewConfig()
	config.ComparisonScript = writeScript(t, `import "unsupported";`)

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
	assert.Equal(t, []string{"field", "message", "rpc"}, exports)
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

func TestConnectComparatorsDeleteSuccessfulFields(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.ignored", () => true);
		spectre.field("test.v1.Response.roles", (reference, candidate) =>
			reference.slice().sort().join("\0") === candidate.slice().sort().join("\0"));
	`)
	reference := connectResponse(`{"stable":"same","ignored":"first","roles":["reader","writer"]}`)
	candidate := connectResponse(`{"roles":["writer","reader"],"ignored":"second","stable":"same"}`)

	result := comparator.Compare(
		t.Context(),
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestPreservesProtoJSONInt64AsJavaScriptString(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.count", (reference, candidate) =>
			typeof reference === "string" && typeof candidate === "string");
	`)
	reference := connectResponse(`{"count":"9007199254740993"}`)
	candidate := connectResponse(`{"count":"9007199254740994"}`)

	result := comparator.Compare(
		t.Context(),
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestPassesUndefinedForMissingField(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.ignored", (reference, candidate) =>
			reference === undefined && candidate === "present");
	`)
	reference := connectResponse(`{"stable":"same"}`)
	candidate := connectResponse(`{"stable":"same","ignored":"present"}`)

	result := comparator.Compare(
		t.Context(),
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestChildFailureSurvivesSuccessfulParentComparator(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.ignored", () => false);
		spectre.message("test.v1.Response", (reference, candidate) =>
			reference.ignored !== undefined && candidate.ignored !== undefined);
	`)
	reference := connectResponse(`{"stable":"same","ignored":"first"}`)
	candidate := connectResponse(`{"stable":"same","ignored":"second"}`)

	result := comparator.Compare(
		t.Context(),
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.NewDifferenceResult("$.ignored"), result)
}

func TestParentComparatorReceivesPrunedChildren(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.ignored", () => true);
		spectre.message("test.v1.Response", (reference, candidate) =>
			reference.ignored === undefined && candidate.ignored === undefined);
	`)
	reference := connectResponse(`{"stable":"same","ignored":"first"}`)
	candidate := connectResponse(`{"stable":"same","ignored":"second"}`)

	result := comparator.Compare(
		t.Context(),
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestLogsComparisonAndIndividualComparatorResults(t *testing.T) {
	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	config := comparison.NewConfig()
	config.ComparisonScript = writeScript(t, comparisonModule(`
		spectre.field("test.v1.Response.ignored", () => true);
		spectre.field("test.v1.Response.stable", () => false);
	`))
	comparator, err := comparison.New(t.Context(), config, log)
	assert.NoError(t, err)
	assert.NoError(t, comparator.Configure(t.Context(), descriptorSet()))
	reference := connectResponse(`{"stable":"first","ignored":"first"}`)
	candidate := connectResponse(`{"stable":"second","ignored":"second"}`)

	result := comparator.Compare(
		t.Context(),
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.NewDifferenceResult("$.stable"), result)
	logs := output.String()
	assert.Contains(t, logs, `"msg":"Response comparator completed","kind":"field","target":"test.v1.Response.ignored","response_path":"$.ignored","matched":true`)
	assert.Contains(t, logs, `"msg":"Response comparator completed","kind":"field","target":"test.v1.Response.stable","response_path":"$.stable","matched":false`)
	assert.Contains(t, logs, `"level":"DEBUG","msg":"Response comparison completed","path":"/test.v1.Service/Get","outcome":"divergent","differences":["$.stable"]`)
}

func TestAppliesFieldComparatorToRepeatedMessageElements(t *testing.T) {
	comparator := newComparator(t, `
		spectre.field("test.v1.Response.users[].name", (reference, candidate) =>
			reference.toLowerCase() === candidate.toLowerCase());
	`)
	reference := connectResponse(`{"stable":"same","users":[{"name":"ALICE"},{"name":"BOB"}]}`)
	candidate := connectResponse(`{"stable":"same","users":[{"name":"alice"},{"name":"bob"}]}`)

	result := comparator.Compare(
		t.Context(),
		"/test.v1.Service/Get",
		"application/json",
		reference,
		candidate,
	)

	assert.Equal(t, comparison.Resultf(comparison.Equivalent, ""), result)
}

func TestMessageComparatorCanDeleteExtraRepeatedElement(t *testing.T) {
	comparator := newComparator(t, `
		spectre.message("test.v1.User", (reference, candidate) =>
			candidate === undefined || reference.name.toLowerCase() === candidate.name.toLowerCase());
	`)
	reference := connectResponse(`{"users":[{"name":"ALICE"},{"name":"BOB"},{"name":"ignored"}]}`)
	candidate := connectResponse(`{"users":[{"name":"alice"},{"name":"bob"}]}`)

	result := comparator.Compare(
		t.Context(),
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
		"/test.v1.Service/Get",
		"application/grpc+proto",
		reference,
		candidate,
	)
	assert.Equal(t, comparison.NewDifferenceResult("$.stable"), bodyResult)

	candidate.Header.Set("Grpc-Status", "7")
	statusResult := comparator.Compare(
		t.Context(),
		"/test.v1.Service/Get",
		"application/grpc",
		reference,
		candidate,
	)
	assert.Equal(t, comparison.NewDifferenceResult("$status"), statusResult)

	reference.Header.Set("Grpc-Status", "invalid")
	invalidStatusResult := comparator.Compare(
		t.Context(),
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
		"/test.v1.Service/Watch",
		"application/grpc",
		response,
		response,
	)
	assert.Equal(t, comparison.Skipped, streaming.Outcome())

	grpcWeb := comparator.Compare(
		t.Context(),
		"/test.v1.Service/Get",
		"application/grpc-web+proto",
		response,
		response,
	)
	assert.Equal(t, comparison.Skipped, grpcWeb.Outcome())
}

func TestRejectsInvalidComparatorResultsAndTargets(t *testing.T) {
	comparator := newComparator(t, `
		spectre.rpc("test.v1.Service.Get", () => "yes");
	`)
	response := connectResponse(`{"stable":"same"}`)
	result := comparator.Compare(
		t.Context(),
		"/test.v1.Service/Get",
		"application/json",
		response,
		response,
	)
	assert.Equal(t, comparison.Unable, result.Outcome())

	config := comparison.NewConfig()
	config.ComparisonScript = writeScript(t, comparisonModule(`spectre.field("test.v1.Response.unknown", () => true);`))
	invalid, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	err = invalid.Configure(t.Context(), descriptorSet())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "has no field")
}

func TestInterruptsRunawayComparator(t *testing.T) {
	config := comparison.NewConfig()
	config.ComparisonScript = writeScript(t, comparisonModule(`spectre.rpc("test.v1.Service.Get", () => { while (true) {} });`))
	config.ComparisonTimeout = 10 * time.Millisecond
	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	assert.NoError(t, comparator.Configure(t.Context(), descriptorSet()))
	response := connectResponse(`{"stable":"same"}`)

	result := comparator.Compare(
		t.Context(),
		"/test.v1.Service/Get",
		"application/json",
		response,
		response,
	)

	assert.Equal(t, comparison.Unable, result.Outcome())
}

func newComparator(t *testing.T, script string) *comparison.Comparator {
	t.Helper()
	config := comparison.NewConfig()
	config.ComparisonScript = writeScript(t, comparisonModule(script))
	comparator, err := comparison.New(t.Context(), config, slog.New(slog.DiscardHandler))
	assert.NoError(t, err)
	assert.NoError(t, comparator.Configure(t.Context(), descriptorSet()))
	return comparator
}

func comparisonModule(body string) string {
	return `import * as spectre from "spectre";` + body
}

func writeScript(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comparison.js")
	assert.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	return path
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

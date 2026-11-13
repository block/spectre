package httpserver_test

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/descriptors"
)

const (
	peopleHost      = "people.example"
	employmentsPath = "/people.v1.People/ListEmployments"
	unitsPath       = "/people.v1.People/GetUnits"
)

// peopleScript answers unit lookups from the employments the reference listed.
const peopleScript = `
import * as spectre from "spectre";
import type * as v1 from "people.v1";
const employments = "POST people.example/people.v1.People/ListEmployments";
spectre.egress.http(employments).match<v1.ListEmploymentsRequest>();
spectre.egress
  .http("POST people.example/people.v1.People/GetUnits")
  .derive<v1.GetUnitsRequest, v1.GetUnitsResponse>()
  .from<v1.ListEmploymentsRequest, v1.ListEmploymentsResponse>(
    employments,
    (request) => ({ person: request.person }),
    (request, reference) => ({
      error: "",
      units: (reference.employments ?? []).find((employment) => employment.token === request.employment)?.units ?? [],
    }),
  );
`

func TestDerivesCandidateResponseFromAnotherEndpoint(t *testing.T) {
	for name, test := range map[string]struct {
		contentType string
		listRequest []byte
		listed      []byte
		unitRequest []byte
		expected    []byte
	}{
		"Protobuf": {
			contentType: "application/x-protobuf",
			listRequest: protoStrings(1, "person-1"),
			listed:      protoMessages(2, append(protoStrings(1, "employment-1"), protoStrings(2, "unit-1", "unit-2")...)),
			unitRequest: append(protoStrings(1, "person-1"), protoStrings(2, "employment-1")...),
			expected:    protoStrings(2, "unit-1", "unit-2"),
		},
		"JSON": {
			contentType: "application/json",
			listRequest: []byte(`{"person":"person-1"}`),
			listed:      []byte(`{"error":"","employments":[{"token":"employment-1","units":["unit-1","unit-2"]}]}`),
			unitRequest: []byte(`{"person":"person-1","employment":"employment-1"}`),
			expected:    []byte(`{"error":"","units":["unit-1","unit-2"]}`),
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls := &upstreamCalls{}
			harness := newPeopleHarness(t, calls.handler(test.contentType, "", test.listed))
			harness.Start(t)

			listed := post(t, harness.Reference, employmentsPath, test.contentType, test.listRequest)
			units := post(t, harness.Candidate, unitsPath, test.contentType, test.unitRequest)

			assert.Equal(t, http.StatusOK, listed.Status)
			assert.Equal(t, http.StatusOK, units.Status)
			assert.Equal(t, test.contentType, units.Header.Get("Content-Type"))
			assert.Equal(t, string(test.expected), units.Body)
			assert.Equal(t, []string{employmentsPath}, calls.paths())
			assert.Equal(t, []matchRecord{{Event: "correlation", Protocol: "http", Route: "POST " + peopleHost + unitsPath}},
				logRecords[matchRecord](t, harness.Logs, "Candidate request was derived from a reference request"))
			assert.False(t, strings.Contains(harness.Logs.String(), "Candidate quarantined"))
		})
	}
}

func TestQuarantinesUnusableDerivedResponse(t *testing.T) {
	listed := protoMessages(2, append(protoStrings(1, "employment-1"), protoStrings(2, "unit-1")...))
	unitRequest := append(protoStrings(1, "person-1"), protoStrings(2, "employment-1")...)
	expanded := protoMessages(2, append(protoStrings(1, "employment-1"), protoStrings(2, slices.Repeat([]string{"unit-1"}, 10000)...)...))
	compressed := gzipped(t, expanded)
	for name, test := range map[string]struct {
		contentType string
		unitRequest []byte
		encoding    string
		listed      []byte
		budget      int
		reason      string
	}{
		"GRPC": {
			contentType: "application/grpc",
			unitRequest: grpcFrame(unitRequest),
			listed:      listed,
			reason:      "encode derived response: gRPC requests cannot be answered",
		},
		// The recording fits, but the decoded response must reserve as much again.
		"OverBudget": {
			contentType: "application/x-protobuf",
			unitRequest: unitRequest,
			listed:      listed,
			budget:      len(listed) + 1,
			reason:      "decoded response exceeds the " + strconv.Itoa(len(listed)+1) + " byte recording budget",
		},
		// The decoded response reserves its decompressed size, not its recorded size.
		"CompressedOverBudget": {
			contentType: "application/x-protobuf",
			unitRequest: unitRequest,
			encoding:    "gzip",
			listed:      compressed,
			budget:      2 * len(compressed),
			reason:      "decoded response exceeds the " + strconv.Itoa(2*len(compressed)) + " byte recording budget",
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls := &upstreamCalls{}
			harness := newPeopleHarness(t, calls.handler("application/x-protobuf", test.encoding, test.listed))
			if test.budget > 0 {
				harness.Config.RecordingBufferBytes = test.budget
			}
			harness.Start(t)

			post(t, harness.Reference, employmentsPath, "application/x-protobuf", protoStrings(1, "person-1"))
			units := post(t, harness.Candidate, unitsPath, test.contentType, test.unitRequest)

			assert.Equal(t, http.StatusBadGateway, units.Status)
			assert.Contains(t, harness.QuarantineReason(t), test.reason)
			assert.Equal(t, []string{employmentsPath}, calls.paths())
		})
	}
}

func newPeopleHarness(t *testing.T, handler http.HandlerFunc) *harness {
	t.Helper()
	harness := newHarness(t, newUpstream(t, handler))
	schemaDir, descriptorsDir := writePeopleSchemaAndDescriptors(t)
	harness.Config.Destinations = map[string]string{peopleHost: harness.Config.Destinations[dependencyHost]}
	harness.Config.Descriptors.DescriptorsDir = descriptorsDir
	harness.Engine.Comparison.Schema.SchemaDir = schemaDir
	harness.Engine.Comparison.ScriptsDir = t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(harness.Engine.Comparison.ScriptsDir, "people.ts"), []byte(peopleScript), 0o600))
	return harness
}

// upstreamCalls answers every request with one listing, recording the paths it is asked for.
type upstreamCalls struct {
	mu     sync.Mutex
	called []string
}

func (c *upstreamCalls) handler(contentType, encoding string, body []byte) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		c.mu.Lock()
		c.called = append(c.called, request.URL.Path)
		c.mu.Unlock()
		writer.Header().Set("Content-Type", contentType)
		if encoding != "" {
			writer.Header().Set("Content-Encoding", encoding)
		}
		_, _ = writer.Write(body)
	}
}

func (c *upstreamCalls) paths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.called...)
}

func post(t *testing.T, listener net.Listener, requestPath, contentType string, body []byte) responseView {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+requestPath, bytes.NewReader(body))
	assert.NoError(t, err)
	request.Header.Set("Content-Type", contentType)
	return do(t, request, peopleHost)
}

// protoStrings encodes each value as string field number.
func protoStrings(number protowire.Number, values ...string) []byte {
	encoded := []byte{}
	for _, value := range values {
		encoded = protowire.AppendTag(encoded, number, protowire.BytesType)
		encoded = protowire.AppendString(encoded, value)
	}
	return encoded
}

// protoMessages encodes each message as message field number.
func protoMessages(number protowire.Number, messages ...[]byte) []byte {
	encoded := []byte{}
	for _, message := range messages {
		encoded = protowire.AppendTag(encoded, number, protowire.BytesType)
		encoded = protowire.AppendBytes(encoded, message)
	}
	return encoded
}

func gzipped(t *testing.T, data []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write(data)
	assert.NoError(t, err)
	assert.NoError(t, writer.Close())
	return compressed.Bytes()
}

func grpcFrame(message []byte) []byte {
	frame := make([]byte, 5, 5+len(message))
	binary.BigEndian.PutUint32(frame[1:], uint32(len(message)))
	return append(frame, message...)
}

func writePeopleSchemaAndDescriptors(t *testing.T) (schemaDir string, descriptorsDir string) {
	t.Helper()
	text := func(name string, number int32, repeated bool) *descriptorpb.FieldDescriptorProto {
		field := &descriptorpb.FieldDescriptorProto{Name: new(name), Number: new(number), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}
		if repeated {
			field.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		}
		return field
	}
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name: new("people.proto"), Syntax: new("proto3"), Package: new("people.v1"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("ListEmploymentsRequest"), Field: []*descriptorpb.FieldDescriptorProto{text("person", 1, false)}},
			{Name: new("Employment"), Field: []*descriptorpb.FieldDescriptorProto{text("token", 1, false), text("units", 2, true)}},
			{Name: new("ListEmploymentsResponse"), Field: []*descriptorpb.FieldDescriptorProto{
				text("error", 1, false),
				{
					Name: new("employments"), Number: new(int32(2)), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
					Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: new(".people.v1.Employment"),
				},
			}},
			{Name: new("GetUnitsRequest"), Field: []*descriptorpb.FieldDescriptorProto{text("person", 1, false), text("employment", 2, false)}},
			{Name: new("GetUnitsResponse"), Field: []*descriptorpb.FieldDescriptorProto{text("error", 1, false), text("units", 2, true)}},
		},
	}}}
	declarations, err := descriptors.Declarations(t.Context(), set)
	assert.NoError(t, err)
	schemaDir = t.TempDir()
	for name, declaration := range declarations {
		target := filepath.Join(schemaDir, filepath.FromSlash(name))
		assert.NoError(t, os.MkdirAll(filepath.Dir(target), 0o750))
		assert.NoError(t, os.WriteFile(target, []byte(declaration), 0o600))
	}
	data, err := proto.Marshal(set)
	assert.NoError(t, err)
	descriptorsDir = t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(descriptorsDir, "people.pb"), data, 0o600))
	return schemaDir, descriptorsDir
}

package httpserver_test

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	samplepb "github.com/block/spectre/internal/sample/pb"
)

const (
	usersHost = "users.example"
	listUsers = "/spectre.sample.v1.UserService/ListUsers"
	getUser   = "/spectre.sample.v1.UserService/GetUser"
)

func TestAnswersDerivedRequestFromSourceReference(t *testing.T) {
	var calls atomic.Int32
	upstream := newUpstream(t, func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assert.Equal(t, listUsers, request.URL.Path)
		body, err := proto.Marshal(&samplepb.ListUsersResponse{
			Users:      []*samplepb.User{{Id: "ada", Name: "Ada"}, {Id: "bob", Name: "Bob"}},
			TotalCount: 2,
		})
		assert.NoError(t, err)
		writer.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = writer.Write(body)
	})
	harness := newDerivingHarness(t, upstream)
	harness.Start(t)

	reference := sendProto(t, harness.Reference.Addr().String(), listUsers, &samplepb.ListUsersRequest{Ids: []string{"bob"}})
	candidate := sendProto(t, harness.Candidate.Addr().String(), getUser, &samplepb.GetUserRequest{Id: "bob"})

	assert.Equal(t, http.StatusOK, reference.Status)
	assert.Equal(t, http.StatusOK, candidate.Status)
	assert.Equal(t, "application/x-protobuf", candidate.Header.Get("Content-Type"))
	answer := &samplepb.GetUserResponse{}
	assert.NoError(t, proto.Unmarshal([]byte(candidate.Body), answer))
	assert.Equal(t, "Bob", answer.GetUser().GetName())
	assert.Equal(t, int32(1), calls.Load())
	type answerRecord struct {
		Event  string `json:"event"`
		Route  string `json:"route"`
		Source string `json:"source"`
	}
	assert.Equal(t, []answerRecord{{Event: "correlation", Route: "POST " + usersHost + getUser, Source: "POST " + usersHost + listUsers}},
		logRecords[answerRecord](t, harness.Logs, "Candidate request was answered from a reference response"))
	// The source reference was not claimed, so a candidate request to the source still pairs with it.
	source := sendProto(t, harness.Candidate.Addr().String(), listUsers, &samplepb.ListUsersRequest{Ids: []string{"bob"}})
	assert.Equal(t, reference.Body, source.Body)
}

func TestQuarantinesUnanswerableDerivedRequest(t *testing.T) {
	upstream := newUpstream(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	})
	harness := newDerivingHarness(t, upstream)
	harness.Start(t)

	sendProto(t, harness.Reference.Addr().String(), listUsers, &samplepb.ListUsersRequest{Ids: []string{"bob"}})
	candidate := sendProto(t, harness.Candidate.Addr().String(), getUser, &samplepb.GetUserRequest{Id: "bob"})

	assert.Equal(t, http.StatusBadGateway, candidate.Status)
	assert.Contains(t, harness.QuarantineReason(t), "decode source reference response: response has status 503")
}

// newDerivingHarness answers GetUser from ListUsers references.
func newDerivingHarness(t *testing.T, upstream string) *harness {
	t.Helper()
	harness := newHarness(t, upstream)
	harness.Config.Destinations = map[string]string{usersHost: upstream}
	schemaDir, descriptorsDir := writeDescriptorSet(t, &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		protodesc.ToFileDescriptorProto(samplepb.File_service_proto),
		protodesc.ToFileDescriptorProto(samplepb.File_users_proto),
		protodesc.ToFileDescriptorProto(timestamppb.File_google_protobuf_timestamp_proto),
	}})
	harness.Config.Descriptors.DescriptorsDir = descriptorsDir
	harness.Engine.Comparison.Schema.SchemaDir = schemaDir
	scripts := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(scripts, "users.ts"), []byte(`
import * as spectre from "spectre";
import type { GetUserRequest, GetUserResponse, ListUsersRequest, ListUsersResponse } from "spectre.sample.v1";
const list = "POST `+usersHost+listUsers+`";
spectre.egress.http(list).match<ListUsersRequest>();
spectre.egress.http("POST `+usersHost+getUser+`")
  .derive<GetUserRequest, GetUserResponse>()
  .from<ListUsersRequest, ListUsersResponse>(
    list,
    (request) => ({ ids: [request.id] }),
    (request, reference) => ({ user: reference.users.find((user) => user.id === request.id) }),
  );
`), 0o600))
	harness.Engine.Comparison.ScriptsDir = scripts
	return harness
}

// sendProto makes a bare protobuf request for the users host.
func sendProto(t *testing.T, address, path string, message proto.Message) responseView {
	t.Helper()
	body, err := proto.Marshal(message)
	assert.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, "http://"+address+path, bytes.NewReader(body))
	assert.NoError(t, err)
	request.Header.Set("Content-Type", "application/x-protobuf")
	return do(t, request, usersHost)
}

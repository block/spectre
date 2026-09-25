package sample_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alecthomas/assert/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/block/spectre/internal/sample"
	samplepb "github.com/block/spectre/internal/sample/pb"
	"github.com/block/spectre/internal/sample/pb/samplepbconnect"
	"github.com/block/spectre/internal/schema"
)

func TestUserRPCs(t *testing.T) {
	service, expected := newTestService(t)
	client := newTestClient(t, service)
	startedAt := time.Now()
	response, err := client.ListUsers(t.Context(), connect.NewRequest(&samplepb.ListUsersRequest{}))
	completedAt := time.Now()
	assert.NoError(t, err)
	assertListUsersResponse(t, expected, response.Msg)
	assert.False(t, response.Msg.GetGeneratedAt().AsTime().Before(startedAt))
	assert.False(t, response.Msg.GetGeneratedAt().AsTime().After(completedAt))
	user, err := client.GetUser(t.Context(), connect.NewRequest(&samplepb.GetUserRequest{Id: "user-2"}))
	assert.NoError(t, err)
	assert.True(t, proto.Equal(&samplepb.GetUserResponse{User: expected.GetUsers()[1]}, user.Msg))

	for _, test := range []struct {
		name string
		id   string
		code connect.Code
	}{
		{name: "MissingID", code: connect.CodeInvalidArgument},
		{name: "UnknownID", id: "missing", code: connect.CodeNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.GetUser(t.Context(), connect.NewRequest(&samplepb.GetUserRequest{Id: test.id}))
			assert.Equal(t, test.code, connect.CodeOf(err))
		})
	}
}

func TestServerHealthAndGRPC(t *testing.T) {
	service, expected := newTestService(t)
	mux := http.NewServeMux()
	path, handler := samplepbconnect.NewUserServiceHandler(service)
	mux.Handle(path, handler)
	var logs bytes.Buffer
	server := sample.NewServer(mux, slog.New(slog.NewJSONHandler(&logs, nil)))
	assertHealthStatus(t, server, "/livez", http.StatusNoContent)
	assertHealthStatus(t, server, "/readyz", http.StatusServiceUnavailable)
	assert.Equal(t, "", logs.String())

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx, listener) }()

	for _, path := range []string{"/livez", "/readyz"} {
		response, err := http.Get("http://" + listener.Addr().String() + path)
		assert.NoError(t, err)
		assert.Equal(t, http.StatusNoContent, response.StatusCode)
		assert.NoError(t, response.Body.Close())
	}
	assert.Equal(t, "", logs.String())
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	t.Cleanup(transport.CloseIdleConnections)
	client := samplepbconnect.NewUserServiceClient(
		&http.Client{Transport: transport},
		"http://"+listener.Addr().String(),
		connect.WithGRPC(),
	)
	response, err := client.ListUsers(t.Context(), connect.NewRequest(&samplepb.ListUsersRequest{}))
	assert.NoError(t, err)
	assertListUsersResponse(t, expected, response.Msg)

	cancel()
	select {
	case err := <-serveDone:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("sample server did not stop after cancellation")
	}
	assert.True(t, strings.Contains(logs.String(), `"path":"/spectre.sample.v1.UserService/ListUsers"`))
	logged := logs.String()
	assertHealthStatus(t, server, "/readyz", http.StatusServiceUnavailable)
	assert.Equal(t, logged, logs.String())
}

func TestListFilters(t *testing.T) {
	service, fixture := newTestService(t)
	client := newTestClient(t, service)
	for _, test := range []struct {
		name    string
		request *samplepb.ListUsersRequest
		users   []*samplepb.User
	}{
		{name: "All", request: &samplepb.ListUsersRequest{}, users: fixture.GetUsers()},
		{name: "IDsPreserveFixtureOrder", request: &samplepb.ListUsersRequest{Ids: []string{"user-3", "user-1", "user-1"}}, users: []*samplepb.User{fixture.GetUsers()[0], fixture.GetUsers()[2]}},
		{name: "Role", request: &samplepb.ListUsersRequest{Role: samplepb.Role_ROLE_READER.Enum()}, users: []*samplepb.User{fixture.GetUsers()[1]}},
		{name: "Combined", request: &samplepb.ListUsersRequest{Ids: []string{"user-1"}, Role: samplepb.Role_ROLE_READER.Enum()}},
		{name: "UnknownID", request: &samplepb.ListUsersRequest{Ids: []string{"missing"}}},
		{name: "ExplicitUnspecifiedRole", request: &samplepb.ListUsersRequest{Role: samplepb.Role_ROLE_UNSPECIFIED.Enum()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := client.ListUsers(t.Context(), connect.NewRequest(test.request))
			assert.NoError(t, err)
			expected := &samplepb.ListUsersResponse{
				Users:      test.users,
				TotalCount: int64(len(test.users)),
			}
			assertListUsersResponse(t, expected, response.Msg)
		})
	}
}

func TestResponseIsolation(t *testing.T) {
	service, expected := newTestService(t)
	response, err := service.ListUsers(t.Context(), connect.NewRequest(&samplepb.ListUsersRequest{}))
	assert.NoError(t, err)
	response.Msg.GetUsers()[0].GetLabels()["team"] = "changed"
	response.Msg.GetUsers()[0].GetProfile().GetAddresses()[0].City = "changed"
	response.Msg.GetGeneratedAt().Seconds = 0
	user, err := service.GetUser(t.Context(), connect.NewRequest(&samplepb.GetUserRequest{Id: "user-1"}))
	assert.NoError(t, err)
	user.Msg.GetUser().GetAvatar()[0] = 255
	user.Msg.GetUser().Name = "changed"
	unchanged, err := service.ListUsers(t.Context(), connect.NewRequest(&samplepb.ListUsersRequest{}))
	assert.NoError(t, err)
	assertListUsersResponse(t, expected, unchanged.Msg)
}

func TestRejectInvalidFixture(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
	}{
		{name: "Malformed", data: "{"},
		{name: "UnknownField", data: `{"unknown": true}`},
		{name: "MissingID", data: `{"users": [{}]}`},
		{name: "DuplicateID", data: `{"users": [{"id": "same"}, {"id": "same"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := sample.New([]byte(test.data))
			assert.Error(t, err)
			assert.Equal(t, (*sample.Service)(nil), service)
		})
	}
}

func TestCancelledRequests(t *testing.T) {
	service, _ := newTestService(t)
	for _, test := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		code connect.Code
	}{
		{name: "Cancelled", ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx, cancel
		}, code: connect.CodeCanceled},
		{name: "Expired", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(t.Context(), time.Unix(0, 0))
		}, code: connect.CodeDeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.ctx()
			defer cancel()
			_, err := service.GetUser(ctx, connect.NewRequest(&samplepb.GetUserRequest{Id: "user-1"}))
			assert.Equal(t, test.code, connect.CodeOf(err))
			_, err = service.ListUsers(ctx, connect.NewRequest(&samplepb.ListUsersRequest{}))
			assert.Equal(t, test.code, connect.CodeOf(err))
		})
	}
}

func TestSampleDescriptorAndEncodings(t *testing.T) {
	data, err := os.ReadFile("../../dist/sample.pb")
	assert.NoError(t, err)
	loaded, err := schema.New(data)
	assert.NoError(t, err)
	getUser, err := loaded.Method("spectre.sample.v1.UserService.GetUser")
	assert.NoError(t, err)
	assert.Equal(t, "spectre.sample.v1.GetUserResponse", string(getUser.Output().FullName()))
	listUsers, err := loaded.Method("spectre.sample.v1.UserService.ListUsers")
	assert.NoError(t, err)
	service, fixture := newTestService(t)
	client := newTestClient(t, service)
	response, err := client.ListUsers(t.Context(), connect.NewRequest(&samplepb.ListUsersRequest{}))
	assert.NoError(t, err)
	assert.Equal(t, uint64(9007199254740993), response.Msg.GetUsers()[0].GetRevision())
	assert.Equal(t, uint64(18446744073709551615), response.Msg.GetUsers()[1].GetRevision())
	assert.True(t, response.Msg.GetUsers()[0].Nickname != nil)
	assert.True(t, response.Msg.GetUsers()[1].Nickname == nil)
	assert.True(t, response.Msg.GetUsers()[0].GetProfile().MarketingConsent != nil)
	assert.True(t, response.Msg.GetUsers()[1].GetProfile().MarketingConsent == nil)
	assert.Equal(t, []byte{0, 1, 2, 3, 255}, response.Msg.GetUsers()[0].GetAvatar())

	encoded := proto.Clone(response.Msg).(*samplepb.ListUsersResponse)
	encoded.GeneratedAt = fixture.GetGeneratedAt()
	wire, err := proto.Marshal(encoded)
	assert.NoError(t, err)
	binaryMessage := dynamicpb.NewMessage(listUsers.Output())
	assert.NoError(t, proto.Unmarshal(wire, binaryMessage))
	jsonData, err := os.ReadFile("testdata/users.json")
	assert.NoError(t, err)
	jsonMessage := dynamicpb.NewMessage(listUsers.Output())
	assert.NoError(t, protojson.Unmarshal(jsonData, jsonMessage))
	assert.True(t, proto.Equal(binaryMessage, jsonMessage))
}

func newTestService(t *testing.T) (*sample.Service, *samplepb.ListUsersResponse) {
	t.Helper()
	data, err := os.ReadFile("testdata/users.json")
	assert.NoError(t, err)
	service, err := sample.New(data)
	assert.NoError(t, err)
	fixture := &samplepb.ListUsersResponse{}
	assert.NoError(t, protojson.Unmarshal(data, fixture))
	return service, fixture
}

func assertListUsersResponse(t *testing.T, expected, actual *samplepb.ListUsersResponse) {
	t.Helper()
	assert.NoError(t, actual.GetGeneratedAt().CheckValid())
	expected = proto.Clone(expected).(*samplepb.ListUsersResponse)
	expected.GeneratedAt = actual.GetGeneratedAt()
	assert.True(t, proto.Equal(expected, actual))
}

func assertHealthStatus(t *testing.T, handler http.Handler, path string, expected int) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	assert.Equal(t, expected, response.Code)
}

func newTestClient(t *testing.T, service *sample.Service) samplepbconnect.UserServiceClient {
	t.Helper()
	_, handler := samplepbconnect.NewUserServiceHandler(service)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return samplepbconnect.NewUserServiceClient(server.Client(), server.URL)
}

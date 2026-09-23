package sample_test

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/block/spectre/internal/sample"
	samplepb "github.com/block/spectre/internal/sample/pb"
	"github.com/block/spectre/internal/schema"
)

func TestUserRPCs(t *testing.T) {
	service, expected := newTestService(t)
	client := newTestClient(t, service)
	response, err := client.ListUsers(t.Context(), &samplepb.ListUsersRequest{})
	assert.NoError(t, err)
	assert.True(t, proto.Equal(expected, response))
	user, err := client.GetUser(t.Context(), &samplepb.GetUserRequest{Id: "user-2"})
	assert.NoError(t, err)
	assert.True(t, proto.Equal(&samplepb.GetUserResponse{User: expected.GetUsers()[1]}, user))

	for _, test := range []struct {
		name string
		id   string
		code codes.Code
	}{
		{name: "MissingID", code: codes.InvalidArgument},
		{name: "UnknownID", id: "missing", code: codes.NotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.GetUser(t.Context(), &samplepb.GetUserRequest{Id: test.id})
			assert.Equal(t, test.code, status.Code(err))
		})
	}
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
			response, err := client.ListUsers(t.Context(), test.request)
			assert.NoError(t, err)
			expected := &samplepb.ListUsersResponse{
				Users:       test.users,
				GeneratedAt: fixture.GetGeneratedAt(),
				TotalCount:  int64(len(test.users)),
			}
			assert.True(t, proto.Equal(expected, response))
		})
	}
}

func TestResponseIsolation(t *testing.T) {
	service, expected := newTestService(t)
	response, err := service.ListUsers(t.Context(), &samplepb.ListUsersRequest{})
	assert.NoError(t, err)
	response.GetUsers()[0].GetLabels()["team"] = "changed"
	response.GetUsers()[0].GetProfile().GetAddresses()[0].City = "changed"
	response.GetGeneratedAt().Seconds = 0
	user, err := service.GetUser(t.Context(), &samplepb.GetUserRequest{Id: "user-1"})
	assert.NoError(t, err)
	user.GetUser().GetAvatar()[0] = 255
	user.GetUser().Name = "changed"
	unchanged, err := service.ListUsers(t.Context(), &samplepb.ListUsersRequest{})
	assert.NoError(t, err)
	assert.True(t, proto.Equal(expected, unchanged))
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
		code codes.Code
	}{
		{name: "Cancelled", ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx, cancel
		}, code: codes.Canceled},
		{name: "Expired", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(t.Context(), time.Unix(0, 0))
		}, code: codes.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.ctx()
			defer cancel()
			_, err := service.GetUser(ctx, &samplepb.GetUserRequest{Id: "user-1"})
			assert.Equal(t, test.code, status.Code(err))
			_, err = service.ListUsers(ctx, &samplepb.ListUsersRequest{})
			assert.Equal(t, test.code, status.Code(err))
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
	service, _ := newTestService(t)
	client := newTestClient(t, service)
	response, err := client.ListUsers(t.Context(), &samplepb.ListUsersRequest{})
	assert.NoError(t, err)
	assert.Equal(t, uint64(9007199254740993), response.GetUsers()[0].GetRevision())
	assert.Equal(t, uint64(18446744073709551615), response.GetUsers()[1].GetRevision())
	assert.True(t, response.GetUsers()[0].Nickname != nil)
	assert.True(t, response.GetUsers()[1].Nickname == nil)
	assert.True(t, response.GetUsers()[0].GetProfile().MarketingConsent != nil)
	assert.True(t, response.GetUsers()[1].GetProfile().MarketingConsent == nil)
	assert.Equal(t, []byte{0, 1, 2, 3, 255}, response.GetUsers()[0].GetAvatar())

	wire, err := proto.Marshal(response)
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

func newTestClient(t *testing.T, service *sample.Service) samplepb.UserServiceClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	samplepb.RegisterUserServiceServer(server, service)
	// Join the serving goroutine during cleanup so tests cannot leak active RPC work.
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		assert.NoError(t, <-done)
	})
	connection, err := grpc.NewClient("passthrough:///sample",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			conn, err := listener.DialContext(ctx)
			return conn, errors.Wrap(err, "dial sample server")
		}),
	)
	assert.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	return samplepb.NewUserServiceClient(connection)
}

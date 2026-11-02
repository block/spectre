package user

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"

	"example.com/m/api/apiconnect"
)

// Each test constructs a client for its own test server.
func TestListUsers(t *testing.T) {
	client := apiconnect.NewUserServiceClient(connect.NewClient("http://localhost"))
	_ = client.ListUsers(context.Background())
}

package user

import (
	"context"
	"net/http"
	"testing"

	"example.com/m/api/apiconnect"
)

// Each test constructs a client for its own test server.
func TestListUsers(t *testing.T) {
	client := apiconnect.NewUserServiceClient(&http.Client{}, "http://localhost")
	_ = client.ListUsers(context.Background())
}

package main

import (
	"context"

	"connectrpc.com/connect/v2"

	"example.com/m/api/apiconnect"
)

// main runs once per process, so it constructs the client once.
func main() {
	client := apiconnect.NewUserServiceClient(connect.NewClient("http://localhost"))
	_ = client.ListUsers(context.Background())
}

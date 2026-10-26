package main

import (
	"context"
	"net/http"

	"example.com/m/api/apiconnect"
)

// main runs once per process, so it constructs the client once.
func main() {
	client := apiconnect.NewUserServiceClient(&http.Client{}, "http://localhost")
	_ = client.ListUsers(context.Background())
}

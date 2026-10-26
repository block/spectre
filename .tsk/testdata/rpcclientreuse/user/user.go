package user

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	"example.com/m/api/apiconnect"
)

func ListUsers(ctx context.Context, url string) error {
	client := apiconnect.NewUserServiceClient(&http.Client{}, url) // want `apiconnect.NewUserServiceClient builds a client on every call; construct the client once, wrap it in a struct with methods, and pass that in`
	return client.ListUsers(ctx)
}

func ListAndClearUsers(ctx context.Context, url string) error {
	var client = apiconnect.NewUserServiceClient(&http.Client{}, url) // want `builds a client on every call`
	if err := client.ListUsers(ctx); err != nil {
		return err
	}
	return client.ClearUsers(ctx)
}

func ClearUsers(ctx context.Context, url string) error {
	return apiconnect.NewUserServiceClient(&http.Client{}, url).ClearUsers(ctx) // want `builds a client on every call`
}

func Handler(url string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client := apiconnect.NewUserServiceClient(&http.Client{}, url) // want `builds a client on every call`
		_ = client.ListUsers(r.Context())
	}
}

// Client is constructed once and reused.
type Client struct {
	client apiconnect.UserServiceClient
}

func New(url string) *Client {
	return &Client{client: apiconnect.NewUserServiceClient(&http.Client{}, url)}
}

func NewThroughVariable(url string) *Client {
	client := apiconnect.NewUserServiceClient(&http.Client{}, url)
	return &Client{client: client}
}

func NewStored(url string) *Client {
	c := &Client{}
	c.client = apiconnect.NewUserServiceClient(&http.Client{}, url)
	return c
}

func NewService(url string) apiconnect.UserServiceClient {
	return apiconnect.NewUserServiceClient(&http.Client{}, url)
}

func NewReturned(url string) apiconnect.UserServiceClient {
	client := apiconnect.NewUserServiceClient(&http.Client{}, url)
	return client
}

func ListPassed(ctx context.Context, url string) error {
	client := apiconnect.NewUserServiceClient(&http.Client{}, url)
	if err := client.ListUsers(ctx); err != nil {
		return err
	}
	return clear(ctx, client)
}

func clear(ctx context.Context, client apiconnect.UserServiceClient) error {
	return client.ClearUsers(ctx)
}

// A closure that outlives the call keeps the client for every later call.
func NewLister(url string) func(ctx context.Context) error {
	client := apiconnect.NewUserServiceClient(&http.Client{}, url)
	return func(ctx context.Context) error { return client.ListUsers(ctx) }
}

func (c *Client) Run(ctx context.Context, url string) error {
	client := apiconnect.NewUserServiceClient(&http.Client{}, url)
	return client.ListUsers(ctx)
}

type cacheClient interface{ Get() error }

type cache struct{}

func (cache) Get() error { return nil }

// NewCacheClient is not generated: its package path does not end in connect.
func NewCacheClient(httpClient connect.HTTPClient) cacheClient { return cache{} }

func Get(httpClient connect.HTTPClient) error {
	client := NewCacheClient(httpClient)
	return client.Get()
}

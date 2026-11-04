// Package client calls the SPECTRE server.
package client

import (
	"context"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/wire"
	pb "github.com/block/spectre/spectrepb"
	"github.com/block/spectre/spectrepb/spectrepbconnect"
)

// heartbeatTimeout bounds one heartbeat, so a slow server never delays the next.
const heartbeatTimeout = 10 * time.Second

// Config locates the SPECTRE server.
type Config struct {
	URL string `name:"server-url" default:"${server_url}" placeholder:"URL" help:"Base URL of the SPECTRE server."`
	// Socket routes every call through a unix socket, such as a service mesh's
	// egress socket, which routes by the URL's host.
	Socket string `name:"server-socket" placeholder:"PATH" help:"Unix socket to reach the SPECTRE server through, such as a service mesh's egress socket: an absolute path or an @ abstract name. The mesh routes by the --server-url host."`
}

// Validate checks that a server URL is set. Kong cannot require the flag,
// because its default, even when empty, counts as set.
func (c Config) Validate() error {
	if c.URL == "" {
		return errors.New("no SPECTRE server URL; pass --server-url, or build spectre with spectre.WithServerURL")
	}
	return nil
}

// Client calls the SPECTRE server.
type Client struct {
	rpc spectrepbconnect.SpectreServiceClient
}

// New returns a client of the SPECTRE server that config locates.
func New(config Config) *Client {
	httpClient := &http.Client{}
	if config.Socket != "" {
		transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // The default transport is always an *http.Transport.
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", config.Socket)
		}
		httpClient.Transport = transport
	}
	return &Client{rpc: spectrepbconnect.NewSpectreServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, config.URL)))}
}

// Update sends a mirror's latest state. It does not retry, because the
// next heartbeat replaces it.
func (c *Client) Update(ctx context.Context, state model.State) error {
	ctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()
	_, err := c.rpc.Update(ctx, &pb.UpdateRequest{State: wire.StateMessage(state)})
	return errors.Wrap(err, "send heartbeat")
}

// ListDivergences returns the states of the reporting mirrors and the
// recorded divergences that filter selects, ordered by source.
func (c *Client) ListDivergences(ctx context.Context, filter model.DivergenceFilter) ([]model.State, []model.Divergence, error) {
	response, err := c.rpc.ListDivergences(ctx, wire.FilterMessage(filter))
	if err != nil {
		return nil, nil, errors.Wrap(err, "list divergences")
	}
	states, divergences, err := wire.ListFromResponse(response)
	return states, divergences, errors.Wrap(err, "list divergences")
}

// ClearDivergences removes the divergences of one service environment.
func (c *Client) ClearDivergences(ctx context.Context, service, environment string) error {
	request := &pb.ClearDivergencesRequest{Service: service, Environment: environment}
	_, err := c.rpc.ClearDivergences(ctx, request)
	return errors.Wrap(err, "clear divergences")
}

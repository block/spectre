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

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/wire"
	pb "github.com/block/spectre/spectrepb"
	"github.com/block/spectre/spectrepb/spectrepbconnect"
)

const (
	attemptTimeout = 10 * time.Second
	minRetryDelay  = time.Second
	maxRetryDelay  = time.Minute
)

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

// Quarantine records a mirror's quarantine. A mirror reports each one only once,
// so failures retry with backoff until the server accepts or rejects it.
func (c *Client) Quarantine(ctx context.Context, divergence model.Divergence) error {
	request := &pb.QuarantineRequest{Divergence: wire.DivergenceMessage(divergence)}
	for delay := minRetryDelay; ; delay = min(delay*2, maxRetryDelay) {
		err := c.quarantineOnce(ctx, request)
		if err == nil || ctx.Err() != nil || connect.CodeOf(err) == connect.CodeInvalidArgument {
			return err
		}
		logger.FromContext(ctx).WarnContext(ctx, "Failed to report quarantine to the SPECTRE server", "error", err, "retry", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		}
	}
}

func (c *Client) quarantineOnce(ctx context.Context, request *pb.QuarantineRequest) error {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	_, err := c.rpc.Quarantine(ctx, request)
	return errors.Wrap(err, "report quarantine")
}

// ListDivergences returns the divergences that filter selects, ordered by source.
func (c *Client) ListDivergences(ctx context.Context, filter model.DivergenceFilter) ([]model.Divergence, error) {
	response, err := c.rpc.ListDivergences(ctx, wire.FilterMessage(filter))
	if err != nil {
		return nil, errors.Wrap(err, "list divergences")
	}
	divergences := make([]model.Divergence, 0, len(response.GetDivergences()))
	for _, message := range response.GetDivergences() {
		divergence, err := wire.DivergenceFromMessage(message)
		if err != nil {
			return nil, errors.Wrap(err, "list divergences")
		}
		divergences = append(divergences, divergence)
	}
	return divergences, nil
}

// ClearDivergences removes the divergences of one service environment.
func (c *Client) ClearDivergences(ctx context.Context, service, environment string) error {
	request := &pb.ClearDivergencesRequest{Service: service, Environment: environment}
	_, err := c.rpc.ClearDivergences(ctx, request)
	return errors.Wrap(err, "clear divergences")
}

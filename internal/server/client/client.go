// Package client calls the SPECTRE server.
package client

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/logger"
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
	URL string `name:"server-url" required:"" placeholder:"URL" help:"Base URL of the SPECTRE server."`
}

// Client calls the SPECTRE server.
type Client struct {
	rpc spectrepbconnect.SpectreServiceClient
}

// New returns a client of the SPECTRE server that config locates.
func New(config Config) *Client {
	return &Client{rpc: spectrepbconnect.NewSpectreServiceClient(&http.Client{}, config.URL)}
}

// Quarantine records a mirror's quarantine. A mirror reports each one only once,
// so failures retry with backoff until the server accepts or rejects it.
func (c *Client) Quarantine(ctx context.Context, divergence *pb.Divergence) error {
	request := &pb.QuarantineRequest{Divergence: divergence}
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
	_, err := c.rpc.Quarantine(ctx, connect.NewRequest(request))
	return errors.Wrap(err, "report quarantine")
}

// ListDivergences returns the divergences that match request's filters.
func (c *Client) ListDivergences(ctx context.Context, request *pb.ListDivergencesRequest) (*pb.ListDivergencesResponse, error) {
	response, err := c.rpc.ListDivergences(ctx, connect.NewRequest(request))
	if err != nil {
		return nil, errors.Wrap(err, "list divergences")
	}
	return response.Msg, nil
}

// ClearDivergences removes the divergences of one service environment.
func (c *Client) ClearDivergences(ctx context.Context, service, environment string) error {
	request := &pb.ClearDivergencesRequest{Service: service, Environment: environment}
	_, err := c.rpc.ClearDivergences(ctx, connect.NewRequest(request))
	return errors.Wrap(err, "clear divergences")
}

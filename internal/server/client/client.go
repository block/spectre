// Package client calls the SPECTRE server.
package client

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
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
	_, err := c.rpc.Quarantine(ctx, connect.NewRequest(request))
	return errors.Wrap(err, "report quarantine")
}

// ListDivergences returns the divergences that filter selects, ordered by source.
func (c *Client) ListDivergences(ctx context.Context, filter model.DivergenceFilter) ([]model.Divergence, error) {
	response, err := c.rpc.ListDivergences(ctx, connect.NewRequest(wire.FilterMessage(filter)))
	if err != nil {
		return nil, errors.Wrap(err, "list divergences")
	}
	divergences := make([]model.Divergence, 0, len(response.Msg.GetDivergences()))
	for _, message := range response.Msg.GetDivergences() {
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
	_, err := c.rpc.ClearDivergences(ctx, connect.NewRequest(request))
	return errors.Wrap(err, "clear divergences")
}

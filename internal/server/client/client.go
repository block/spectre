// Package client calls the SPECTRE server.
package client

import (
	"context"
	"io"
	"net"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/wire"
	pb "github.com/block/spectre/spectrepb"
	"github.com/block/spectre/spectrepb/spectrepbconnect"
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

// Update opens a stream of a mirror's state that begins with its whole state.
// The stream lasts until ctx ends or it is closed.
func (c *Client) Update(ctx context.Context, state model.State) (*Updates, error) {
	stream, err := c.rpc.Update(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "open update stream")
	}
	updates := newUpdates(stream)
	if err := updates.send(&pb.UpdateRequest{Update: &pb.UpdateRequest_State{State: wire.StateMessage(state)}}); err != nil {
		return nil, err
	}
	return updates, nil
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

// Updates streams changes to a mirror's state to the SPECTRE server.
type Updates struct {
	stream spectrepbconnect.SpectreServiceUpdateClientStream
}

func newUpdates(stream spectrepbconnect.SpectreServiceUpdateClientStream) *Updates {
	return &Updates{stream: stream}
}

// Quarantine adds the quarantine of the proxy in direction to the state.
func (u *Updates) Quarantine(direction model.Direction, quarantine model.Quarantine) error {
	return u.send(&pb.UpdateRequest{Update: &pb.UpdateRequest_Quarantine{Quarantine: wire.QuarantineMessage(direction, quarantine)}})
}

// Keepalive shows the server that the mirror still runs.
func (u *Updates) Keepalive() error {
	return u.send(&pb.UpdateRequest{Update: &pb.UpdateRequest_Keepalive{Keepalive: &pb.Keepalive{}}})
}

// Close ends the stream and returns the error that ended it on the server, if any.
func (u *Updates) Close() error {
	_, err := u.stream.CloseAndReceive()
	return errors.Wrap(err, "close update stream")
}

// send sends an update. A send fails with io.EOF once the server has ended the
// stream, so the error comes from closing it.
func (u *Updates) send(request *pb.UpdateRequest) error {
	err := u.stream.Send(request)
	if errors.Is(err, io.EOF) {
		if err := u.Close(); err != nil {
			return err
		}
		return errors.New("the SPECTRE server ended the update stream")
	}
	return errors.Wrap(err, "send update")
}

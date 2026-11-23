package client

import (
	"context"
	"slices"

	"github.com/alecthomas/errors"
	"golang.org/x/sync/errgroup"

	"github.com/block/spectre/internal/model"
)

// ServersConfig locates the SPECTRE servers a command lists, such as the
// servers of every deployment.
type ServersConfig struct {
	URLs   []string `name:"server-url" default:"${collector_servers}" placeholder:"URL" help:"Base URL of a SPECTRE server. Repeat it to list and clear the mirrors of several servers."`
	Socket string   `name:"server-socket" placeholder:"PATH" help:"Unix socket to reach the SPECTRE servers through, such as a service mesh's egress socket: an absolute path or an @ abstract name. The mesh routes by each --server-url host."`
}

// Validate checks that at least one server URL is set and none is empty.
func (c ServersConfig) Validate() error {
	if len(c.URLs) == 0 || slices.Contains(c.URLs, "") {
		return errors.New("no SPECTRE server URL; pass --server-url, or build spectre with spectre.WithServerURL")
	}
	return nil
}

// Servers calls several SPECTRE servers as one.
type Servers struct {
	urls    []string
	clients []*Client
}

// NewServers returns clients of the servers that config locates.
func NewServers(config ServersConfig) *Servers {
	servers := &Servers{}
	for _, url := range config.URLs {
		servers.urls = append(servers.urls, url)
		servers.clients = append(servers.clients, New(Config{URL: url, Socket: config.Socket}))
	}
	return servers
}

// ListDivergences returns what filter selects on every server, in server
// order. It fails if any server does, so no server's mirrors go missing.
func (s *Servers) ListDivergences(ctx context.Context, filter model.DivergenceFilter) (model.Listing, error) {
	listings := make([]model.Listing, len(s.clients))
	group, ctx := errgroup.WithContext(ctx)
	for i, client := range s.clients {
		group.Go(func() (err error) {
			listings[i], err = client.ListDivergences(ctx, filter)
			return errors.Wrap(err, s.urls[i])
		})
	}
	if err := group.Wait(); err != nil {
		return model.Listing{}, errors.WithStack(err)
	}
	var listing model.Listing
	for _, listed := range listings {
		listing.States = append(listing.States, listed.States...)
		listing.Divergences = append(listing.Divergences, listed.Divergences...)
	}
	return listing, nil
}

// ClearDivergences removes the divergences of one service environment from
// every server. A server that holds none of them has nothing to remove.
func (s *Servers) ClearDivergences(ctx context.Context, service, environment string) error {
	group, ctx := errgroup.WithContext(ctx)
	for i, client := range s.clients {
		group.Go(func() error {
			return errors.Wrap(client.ClearDivergences(ctx, service, environment), s.urls[i])
		})
	}
	return errors.WithStack(group.Wait())
}

package client

import (
	"context"
	"slices"
	"sync"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/model"
)

// ServersConfig locates the SPECTRE servers that spectre status calls.
type ServersConfig struct {
	URLs []string `name:"server-url" default:"${status_server_urls}" sep:"," placeholder:"URL" help:"Base URL of a SPECTRE server. Repeat it, or separate URLs with commas, to call several servers."`
	// Socket routes every call through a unix socket, such as a service mesh's
	// egress socket, which routes by each URL's host.
	Socket string `name:"server-socket" placeholder:"PATH" help:"Unix socket to reach the SPECTRE servers through, such as a service mesh's egress socket: an absolute path or an @ abstract name. The mesh routes by the --server-url host."`
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

// NewServers returns a client of each distinct server that config locates.
func NewServers(config ServersConfig) *Servers {
	servers := &Servers{}
	for _, url := range config.URLs {
		if slices.Contains(servers.urls, url) {
			continue
		}
		servers.urls = append(servers.urls, url)
		servers.clients = append(servers.clients, New(Config{URL: url, Socket: config.Socket}))
	}
	return servers
}

// ListDivergences merges what every server lists, ordered as one server orders
// it. A failed server's URL names its error, and the others still list.
func (s *Servers) ListDivergences(ctx context.Context, filter model.DivergenceFilter) ([]model.State, []model.Divergence, error) {
	type listed struct {
		states      []model.State
		divergences []model.Divergence
		err         error
	}
	results := make([]listed, len(s.clients))
	var wg sync.WaitGroup
	for i, client := range s.clients {
		wg.Go(func() {
			states, divergences, err := client.ListDivergences(ctx, filter)
			results[i] = listed{states: states, divergences: divergences, err: errors.Wrap(err, s.urls[i])}
		})
	}
	wg.Wait()
	states := []model.State{}
	divergences := []model.Divergence{}
	errs := []error{}
	for _, result := range results {
		states = append(states, result.states...)
		divergences = append(divergences, result.divergences...)
		if result.err != nil {
			errs = append(errs, result.err)
		}
	}
	slices.SortStableFunc(states, model.CompareStates)
	slices.SortStableFunc(divergences, model.CompareDivergences)
	return states, divergences, errors.Join(errs...)
}

// ClearDivergences clears the divergences of one service environment on every
// server, because only the server its mirrors report to holds them.
func (s *Servers) ClearDivergences(ctx context.Context, service, environment string) error {
	errs := make([]error, len(s.clients))
	var wg sync.WaitGroup
	for i, client := range s.clients {
		wg.Go(func() {
			errs[i] = errors.Wrap(client.ClearDivergences(ctx, service, environment), s.urls[i])
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

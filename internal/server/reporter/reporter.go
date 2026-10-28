// Package reporter reports a mirror's quarantines to the SPECTRE server.
package reporter

import (
	"context"
	"sync"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/client"
)

// Config configures how a mirror reports quarantines to the SPECTRE server.
type Config struct {
	Client      client.Config `embed:""`
	Service     string        `required:"" help:"Service the mirror runs, reported to the SPECTRE server."`
	Environment string        `required:"" help:"Environment the mirror runs in, such as production-us-west-2, reported to the SPECTRE server."`
	Revision    string        `required:"" help:"Commit of the candidate the mirror runs, reported to the SPECTRE server."`
}

// Source is a proxy whose quarantine a mirror reports.
type Source struct {
	Direction model.Direction
	// Quarantined receives the proxy's quarantine once, when it begins.
	Quarantined <-chan model.Quarantine
}

// Reporter sends each quarantine of a mirror's sources to the SPECTRE server.
type Reporter struct {
	config  Config
	client  *client.Client
	sources []Source
}

// New reports the quarantines of a mirror's sources through server.
func New(config Config, server *client.Client, sources ...Source) *Reporter {
	return &Reporter{config: config, client: server, sources: sources}
}

// Run reports each source's quarantine until ctx is cancelled. A quarantine
// not yet reported when ctx is cancelled is lost.
func (r *Reporter) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, source := range r.sources {
		wg.Go(func() { r.reportSource(ctx, source) })
	}
	wg.Wait()
}

// reportSource waits for the source to quarantine, then reports it. A failed
// report is only logged, so the SPECTRE server never blocks the mirror.
func (r *Reporter) reportSource(ctx context.Context, source Source) {
	var status model.Quarantine
	select {
	case status = <-source.Quarantined:
	case <-ctx.Done():
		return
	}
	divergence := model.Divergence{
		Service:     r.config.Service,
		Environment: r.config.Environment,
		Revision:    r.config.Revision,
		Direction:   source.Direction,
		Quarantine:  status,
	}
	if err := r.client.Quarantine(ctx, divergence); err != nil && ctx.Err() == nil {
		logger.FromContext(ctx).ErrorContext(ctx, "Failed to report quarantine to the SPECTRE server", "error", err)
	}
}

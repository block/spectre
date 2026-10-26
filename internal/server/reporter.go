package server

import (
	"context"
	"sync"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/server/client"
	pb "github.com/block/spectre/spectrepb"
)

// Source is a proxy whose quarantine a mirror reports.
type Source struct {
	Direction boundary.Direction
	// Quarantined receives the proxy's quarantine once, when it begins.
	Quarantined <-chan proxy.QuarantineStatus
}

// Reporter sends each quarantine of a mirror's sources to the SPECTRE server.
type Reporter struct {
	config  ReporterConfig
	client  *client.Client
	sources []Source
}

// NewReporter reports the quarantines of a mirror's sources through server.
func NewReporter(config ReporterConfig, server *client.Client, sources ...Source) *Reporter {
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
	var status proxy.QuarantineStatus
	select {
	case status = <-source.Quarantined:
	case <-ctx.Done():
		return
	}
	divergence := &pb.Divergence{
		Service:     r.config.Service,
		Environment: r.config.Environment,
		Revision:    r.config.Revision,
		Direction:   directionMessage(source.Direction),
		Quarantine:  quarantineMessage(status),
	}
	if err := r.client.Quarantine(ctx, divergence); err != nil && ctx.Err() == nil {
		logger.FromContext(ctx).ErrorContext(ctx, "Failed to report quarantine to the SPECTRE server", "error", err)
	}
}

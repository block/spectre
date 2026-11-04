// Package reporter sends a mirror's heartbeats to the SPECTRE server.
package reporter

import (
	"context"
	"maps"
	"time"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/client"
)

// Config configures how a mirror reports its state to the SPECTRE server.
type Config struct {
	Client            client.Config `embed:""`
	Service           string        `required:"" help:"Service the mirror runs, reported to the SPECTRE server."`
	Environment       string        `required:"" help:"Environment the mirror runs in, such as production-us-west-2, reported to the SPECTRE server."`
	CandidateCommit   string        `required:"" help:"Commit of the candidate the mirror runs, reported to the SPECTRE server."`
	HeartbeatInterval time.Duration `default:"30s" help:"How often the mirror sends its state to the SPECTRE server."`
}

// Source is a proxy whose quarantine a mirror reports.
type Source struct {
	Direction model.Direction
	// Quarantined receives the proxy's quarantine once, when it begins.
	Quarantined <-chan model.Quarantine
}

// Reporter sends a mirror's state to the SPECTRE server.
type Reporter struct {
	config  Config
	client  *client.Client
	sources []Source
}

// New reports the state of a mirror and its sources through server.
func New(config Config, server *client.Client, sources ...Source) *Reporter {
	return &Reporter{config: config, client: server, sources: sources}
}

// sourceQuarantine is a quarantine of the source in direction.
type sourceQuarantine struct {
	direction  model.Direction
	quarantine model.Quarantine
}

// Run sends a heartbeat at once, at every interval, and when a source
// quarantines, until ctx is cancelled.
func (r *Reporter) Run(ctx context.Context) {
	started := time.Now()
	quarantined := make(chan sourceQuarantine)
	for _, source := range r.sources {
		go forward(ctx, source, quarantined)
	}
	quarantines := map[model.Direction]model.Quarantine{}
	ticker := time.NewTicker(r.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		r.heartbeat(ctx, time.Since(started), quarantines)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case status := <-quarantined:
			quarantines[status.direction] = status.quarantine
		}
	}
}

// heartbeat sends the mirror's state. A failure is only logged, so the
// SPECTRE server never blocks the mirror.
func (r *Reporter) heartbeat(ctx context.Context, uptime time.Duration, quarantines map[model.Direction]model.Quarantine) {
	state := model.State{
		Service:         r.config.Service,
		Environment:     r.config.Environment,
		CandidateCommit: r.config.CandidateCommit,
		SpectreVersion:  internal.Version,
		Uptime:          uptime,
		Quarantines:     maps.Clone(quarantines),
	}
	if err := r.client.Heartbeat(ctx, state); err != nil && ctx.Err() == nil {
		logger.FromContext(ctx).WarnContext(ctx, "Failed to send heartbeat to the SPECTRE server", "error", err)
	}
}

// forward passes the source's quarantine on to quarantined.
func forward(ctx context.Context, source Source, quarantined chan<- sourceQuarantine) {
	select {
	case status := <-source.Quarantined:
		select {
		case quarantined <- sourceQuarantine{direction: source.Direction, quarantine: status}:
		case <-ctx.Done():
		}
	case <-ctx.Done():
	}
}

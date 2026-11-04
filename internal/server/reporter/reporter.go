// Package reporter streams what a mirror sends the SPECTRE server: its state
// and every change to it, over one stream.
package reporter

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/stream"
)

// Config configures how a mirror reports to the SPECTRE server.
type Config struct {
	Client      client.Config `embed:""`
	Stream      stream.Config `embed:"" prefix:"server-"`
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

// Reporter streams a mirror's state to the SPECTRE server.
type Reporter struct {
	config  Config
	client  *client.Client
	sources []Source
	started time.Time

	mu          sync.Mutex
	quarantines map[model.Direction]model.Quarantine
}

// New reports the state of a mirror and its sources through server.
func New(config Config, server *client.Client, sources ...Source) *Reporter {
	return &Reporter{config: config, client: server, sources: sources, started: time.Now(), quarantines: map[model.Direction]model.Quarantine{}}
}

// Run streams the mirror's state until ctx is cancelled. Every stream begins
// with the whole state, then sends each quarantine as it begins.
func (r *Reporter) Run(ctx context.Context) {
	dial := func(ctx context.Context) (stream.Conn[model.Update], error) {
		return r.client.OpenUpdates(ctx)
	}
	resync := func() []model.Update { return []model.Update{r.state()} }
	channel := stream.NewOutChannel(r.config.Stream, dial, resync)
	for _, source := range r.sources {
		go r.report(ctx, source, channel)
	}
	channel.Run(ctx)
}

// report sends the source's quarantine once it begins. The state holds it too,
// so a stream that fails before sending it resyncs it.
func (r *Reporter) report(ctx context.Context, source Source, channel *stream.OutChannel[model.Update]) {
	select {
	case quarantine := <-source.Quarantined:
		r.mu.Lock()
		r.quarantines[source.Direction] = quarantine
		r.mu.Unlock()
		if !channel.Send(model.QuarantineUpdate{Direction: source.Direction, Quarantine: quarantine}) {
			logger.FromContext(ctx).WarnContext(ctx, "Dropped a quarantine update for the SPECTRE server; the next stream resyncs it")
		}
	case <-ctx.Done():
	}
}

// state returns the mirror's whole state.
func (r *Reporter) state() model.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return model.State{
		Service:        r.config.Service,
		Environment:    r.config.Environment,
		Revision:       r.config.Revision,
		SpectreVersion: internal.Version,
		Uptime:         time.Since(r.started),
		Quarantines:    maps.Clone(r.quarantines),
	}
}

// Package reporter streams a mirror's state to the SPECTRE server.
package reporter

import (
	"context"
	"maps"
	"math/rand/v2"
	"time"

	"github.com/alecthomas/errors"

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
	Revision          string        `required:"" help:"Commit of the candidate the mirror runs, reported to the SPECTRE server."`
	KeepaliveInterval time.Duration `default:"30s" help:"How often the mirror shows the SPECTRE server it still runs when nothing has changed. Keep it below the server's --state-ttl."`
}

// Source is a proxy whose quarantine a mirror reports.
type Source struct {
	Direction model.Direction
	// Quarantined receives the proxy's quarantine once, when it begins.
	Quarantined <-chan model.Quarantine
}

// Retries of a lost update stream back off between these delays.
const (
	minRetryDelay = time.Second
	maxRetryDelay = time.Minute
)

// Reporter streams a mirror's state to the SPECTRE server.
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

// Run streams the mirror's state until ctx is cancelled, reconnecting with
// backoff whenever the stream ends. Failures are only logged.
func (r *Reporter) Run(ctx context.Context) {
	started := time.Now()
	quarantined := make(chan sourceQuarantine)
	for _, source := range r.sources {
		go forward(ctx, source, quarantined)
	}
	quarantines := map[model.Direction]model.Quarantine{}
	delay := minRetryDelay
	for {
		opened := time.Now()
		err := r.stream(ctx, started, quarantines, quarantined)
		if ctx.Err() != nil {
			return
		}
		// Only consecutive quick failures back off further.
		if time.Since(opened) > maxRetryDelay {
			delay = minRetryDelay
		}
		wait := delay/2 + rand.N(delay/2)
		logger.FromContext(ctx).WarnContext(ctx, "Lost the update stream to the SPECTRE server", "error", err, "retry", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case status := <-quarantined:
			// Reconnect at once, so the server learns of the quarantine.
			quarantines[status.direction] = status.quarantine
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

// stream sends the mirror's whole state, then each quarantine as it begins and
// a keepalive at every interval, until the stream fails or ctx ends.
func (r *Reporter) stream(ctx context.Context, started time.Time, quarantines map[model.Direction]model.Quarantine, quarantined <-chan sourceQuarantine) error {
	updates, err := r.client.Update(ctx, r.state(time.Since(started), quarantines))
	if err != nil {
		return errors.WithStack(err)
	}
	defer updates.Close() //nolint:errcheck // The stream has already failed or its context has ended.
	ticker := time.NewTicker(r.config.KeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		case <-ticker.C:
			err = updates.Keepalive()
		case status := <-quarantined:
			quarantines[status.direction] = status.quarantine
			err = updates.Quarantine(status.direction, status.quarantine)
		}
		if err != nil {
			return errors.WithStack(err)
		}
	}
}

// state returns the mirror's whole state.
func (r *Reporter) state(uptime time.Duration, quarantines map[model.Direction]model.Quarantine) model.State {
	return model.State{
		Service:        r.config.Service,
		Environment:    r.config.Environment,
		Revision:       r.config.Revision,
		SpectreVersion: internal.Version,
		Uptime:         uptime,
		Quarantines:    maps.Clone(quarantines),
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

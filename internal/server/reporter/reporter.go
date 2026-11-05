// Package reporter streams what a mirror sends the SPECTRE server over one
// stream. Every stream begins with the mirror's whole state, then sends each
// quarantine as it begins, so a stream may fail at any time without losing
// state. Every keepalive interval it sends the call counts if they changed,
// or else a keepalive. A failed stream is replaced with backoff, and a stream
// is replaced when it ages, so streams spread across servers.
package reporter

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/server/client"
)

// Config configures how a mirror reports to the SPECTRE server.
type Config struct {
	Client            client.Config `embed:""`
	KeepaliveInterval time.Duration `name:"server-keepalive-interval" default:"30s" help:"How often to send changed call counts, or else a keepalive, on a stream to the SPECTRE server that is otherwise quiet."`
	StreamMaxAge      time.Duration `name:"server-stream-max-age" default:"10m" help:"How long a stream to the SPECTRE server lasts, with jitter, before it is replaced, so streams spread across servers."`
	Service           string        `required:"" help:"Service the mirror runs, reported to the SPECTRE server."`
	Environment       string        `required:"" help:"Environment the mirror runs in, such as production-us-west-2, reported to the SPECTRE server."`
	Revision          string        `required:"" help:"Commit of the candidate the mirror runs, reported to the SPECTRE server."`
}

// Validate checks that the stream intervals are positive.
func (c Config) Validate() error {
	if c.KeepaliveInterval <= 0 || c.StreamMaxAge <= 0 {
		return errors.New("server keepalive interval and stream max age must be positive")
	}
	return nil
}

// Retries of a failed stream back off between these delays.
const (
	minRetryDelay = time.Second
	maxRetryDelay = time.Minute
)

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
	// calls returns the requests the ingress proxy has received on each route.
	calls   func() model.Calls
	started time.Time
	// quarantines belongs to the goroutine that runs streams.
	quarantines map[model.Direction]model.Quarantine
}

// New reports the state of a mirror, its call counts, and its sources through server.
func New(config Config, server *client.Client, calls func() model.Calls, sources ...Source) *Reporter {
	return &Reporter{
		config:      config,
		client:      server,
		sources:     sources,
		calls:       calls,
		started:     time.Now(),
		quarantines: map[model.Direction]model.Quarantine{},
	}
}

// Run keeps a stream open until ctx ends, replacing it when it fails, with
// backoff, or when it ages. Failures are only logged.
func (r *Reporter) Run(ctx context.Context) {
	// Each source sends once, so sending never blocks.
	quarantines := make(chan model.QuarantineUpdate, len(r.sources))
	for _, source := range r.sources {
		go forward(ctx, source, quarantines)
	}
	delay := minRetryDelay
	for {
		opened := time.Now()
		err := r.stream(ctx, quarantines)
		if ctx.Err() != nil {
			return
		}
		// Only consecutive quick failures back off further.
		if err == nil || time.Since(opened) > maxRetryDelay {
			delay = minRetryDelay
		}
		if err == nil {
			continue
		}
		wait := jitter(delay)
		logger.FromContext(ctx).WarnContext(ctx, "Lost the stream to the SPECTRE server", "error", err, "retry", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

// stream sends on one stream until it fails or ctx ends. It returns nil once
// the stream ages and the server accepts it closing.
func (r *Reporter) stream(ctx context.Context, quarantines <-chan model.QuarantineUpdate) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates, err := r.client.OpenUpdates(ctx)
	if err != nil {
		return errors.Wrap(err, "open stream")
	}
	state := r.state()
	if err := updates.Send(state); err != nil {
		return failed(updates, err)
	}
	sent := state.Calls
	keepalive := time.NewTimer(r.config.KeepaliveInterval)
	defer keepalive.Stop()
	age := time.NewTimer(jitter(r.config.StreamMaxAge))
	defer age.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			_ = updates.CloseAndReceive() //nolint:errcheck // Cancelling has already aborted the stream.
			return errors.WithStack(ctx.Err())
		case <-age.C:
			return errors.Wrap(updates.CloseAndReceive(), "close aged stream")
		case <-keepalive.C:
			if calls := r.calls(); !calls.Equal(sent) {
				sent = calls
				err = updates.Send(calls)
			} else {
				err = updates.Keepalive()
			}
		case update := <-quarantines:
			// Recorded first, so the next stream's state holds it if this send fails.
			r.quarantines[update.Direction] = update.Quarantine
			err = updates.Send(update)
		}
		if err != nil {
			return failed(updates, err)
		}
		keepalive.Reset(r.config.KeepaliveInterval)
	}
}

// state returns the mirror's whole state, to send at once.
func (r *Reporter) state() model.State {
	return model.State{
		Service:        r.config.Service,
		Environment:    r.config.Environment,
		Revision:       r.config.Revision,
		SpectreVersion: internal.Version,
		Uptime:         time.Since(r.started),
		Quarantines:    r.quarantines,
		Calls:          r.calls(),
	}
}

// forward sends the source's quarantine to quarantines once it begins.
func forward(ctx context.Context, source Source, quarantines chan<- model.QuarantineUpdate) {
	select {
	case quarantine := <-source.Quarantined:
		quarantines <- model.QuarantineUpdate{Direction: source.Direction, Quarantine: quarantine}
	case <-ctx.Done():
	}
}

// failed closes a stream whose send failed. A send fails once the server has
// ended the stream, and closing returns why it did.
func failed(updates *client.Updates, err error) error {
	if closeErr := updates.CloseAndReceive(); closeErr != nil {
		return errors.Wrap(closeErr, "stream ended")
	}
	return errors.Wrap(err, "send on stream")
}

// jitter returns a random duration between half of d and d.
func jitter(d time.Duration) time.Duration {
	return d/2 + rand.N(d/2+1)
}

package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/boundary"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/proxy"
	pb "github.com/block/spectre/spectrepb"
	"github.com/block/spectre/spectrepb/spectrepbconnect"
)

const (
	attemptTimeout = 10 * time.Second
	minRetryDelay  = time.Second
	maxRetryDelay  = time.Minute
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
	client  spectrepbconnect.SpectreServiceClient
	sources []Source
}

// NewReporter reports the quarantines of a mirror's sources.
func NewReporter(config ReporterConfig, sources ...Source) *Reporter {
	client := spectrepbconnect.NewSpectreServiceClient(&http.Client{}, config.Client.URL)
	return &Reporter{config: config, client: client, sources: sources}
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

// reportSource waits for the source to quarantine, then reports it, retrying
// failures with backoff so the SPECTRE server never blocks the mirror.
func (r *Reporter) reportSource(ctx context.Context, source Source) {
	var status proxy.QuarantineStatus
	select {
	case status = <-source.Quarantined:
	case <-ctx.Done():
		return
	}
	request := &pb.QuarantineRequest{Divergence: &pb.Divergence{
		Service:     r.config.Service,
		Environment: r.config.Environment,
		Revision:    r.config.Revision,
		Direction:   directionMessage(source.Direction),
		Quarantine:  quarantineMessage(status),
	}}
	for delay := minRetryDelay; ; delay = min(delay*2, maxRetryDelay) {
		err := r.send(ctx, request)
		if err == nil || ctx.Err() != nil {
			return
		}
		if connect.CodeOf(err) == connect.CodeInvalidArgument {
			logger.FromContext(ctx).ErrorContext(ctx, "SPECTRE server rejected quarantine", "error", err)
			return
		}
		logger.FromContext(ctx).WarnContext(ctx, "Failed to report quarantine to the SPECTRE server", "error", err, "retry", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
}

func (r *Reporter) send(ctx context.Context, request *pb.QuarantineRequest) error {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	_, err := r.client.Quarantine(ctx, connect.NewRequest(request))
	return errors.Wrap(err, "report quarantine")
}

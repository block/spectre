package mysql

import (
	"context"

	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/protocols"
)

// idle is the server of a mirror that does not proxy MySQL. It is ready at once,
// so it never holds up the mirror's readiness.
type idle struct {
	health *health.Handler
}

// newIdle returns a server that marks health ready, the egress readiness of the mirror, when it serves.
func newIdle(health *health.Handler) *idle {
	return &idle{health: health}
}

func (i *idle) Serve(ctx context.Context, _ protocols.Listeners) error {
	i.health.SetReady(true)
	<-ctx.Done()
	return nil
}

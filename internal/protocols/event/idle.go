package event

import (
	"context"
	"net"

	"github.com/block/spectre/internal/httpservice/health"
)

// idle is the server of a mirror that does not serve events. It is ready at once, so it
// never holds up the mirror's readiness.
type idle struct {
	healths []*health.Handler
}

func newIdle(healths ...*health.Handler) *idle {
	return &idle{healths: healths}
}

func (i *idle) Serve(ctx context.Context, _ map[string]net.Listener) error {
	for _, handler := range i.healths {
		handler.SetReady(true)
	}
	<-ctx.Done()
	return nil
}

//go:build !linux

package supervisor

import (
	"context"
	"net/netip"

	"github.com/alecthomas/errors"
)

// egressRedirect is unavailable off Linux, where the egress sandbox cannot run.
type egressRedirect struct{}

// newEgressRedirect returns a redirect that cannot sandbox off Linux.
func newEgressRedirect(_ context.Context, _ netip.AddrPort) *egressRedirect {
	return &egressRedirect{}
}

// sandbox reports that the egress sandbox is unavailable off Linux.
func (r *egressRedirect) sandbox(_ context.Context, _ *managedProcess) error {
	return errors.New("egress sandbox requires Linux")
}

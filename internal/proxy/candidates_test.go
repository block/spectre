package proxy_test

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/proxy"
)

func TestCandidatesQuarantineStatus(t *testing.T) {
	candidates := proxy.NewCandidates(proxy.CandidatesConfig{CandidateMaxInFlight: 1})

	status := candidates.QuarantineStatus()
	assert.NoError(t, status.Reason)
	assert.True(t, status.Since.IsZero())

	candidates.Quarantine(context.Background(), errors.New("schema gap"))

	status = candidates.QuarantineStatus()
	assert.Error(t, status.Reason)
	assert.Equal(t, "schema gap", status.Reason.Error())
	assert.False(t, status.Since.IsZero())
}

func TestCandidatesQuarantineNilReason(t *testing.T) {
	candidates := proxy.NewCandidates(proxy.CandidatesConfig{CandidateMaxInFlight: 1})

	candidates.Quarantine(context.Background(), nil)

	// A nil reason still quarantines, so the reason is the quarantine signal.
	assert.Error(t, candidates.QuarantineStatus().Reason)
}

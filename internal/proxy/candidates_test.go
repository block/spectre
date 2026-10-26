package proxy_test

import (
	"context"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/quarantine"
)

func TestCandidatesQuarantineStatus(t *testing.T) {
	candidates := proxy.NewCandidates(proxy.CandidatesConfig{CandidateMaxInFlight: 1})

	assert.False(t, candidates.QuarantineStatus().Ok())
	select {
	case <-candidates.Quarantined():
		t.Fatal("healthy candidates reported quarantine")
	default:
	}

	gap := quarantine.Reason{Kind: quarantine.KindUnable, Detail: "schema gap"}
	candidates.Quarantine(context.Background(), gap)
	// Only the first reason is kept.
	candidates.Quarantine(context.Background(), quarantine.Reason{Kind: quarantine.KindCapacity, Detail: "later"})

	<-candidates.Quarantined()
	status, quarantined := candidates.QuarantineStatus().Get()
	assert.True(t, quarantined)
	assert.Equal(t, proxy.QuarantineStatus{Reason: gap}, status, assert.Exclude[time.Time]())
	assert.False(t, status.Since.IsZero())
}

func TestCandidatesConcurrencyLimitQuarantines(t *testing.T) {
	candidates := proxy.NewCandidates(proxy.CandidatesConfig{CandidateMaxInFlight: 1})

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	assert.True(t, candidates.Start(context.Background(), cancel).Ok())
	assert.False(t, candidates.Start(context.Background(), cancel).Ok())

	status, quarantined := candidates.QuarantineStatus().Get()
	assert.True(t, quarantined)
	assert.Equal(t, quarantine.Reason{Kind: quarantine.KindCapacity, Detail: "candidate concurrency limit exceeded"}, status.Reason)
}

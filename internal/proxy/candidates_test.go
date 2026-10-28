package proxy_test

import (
	"context"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/proxy"
)

func TestCandidatesQuarantineStatus(t *testing.T) {
	candidates := proxy.NewCandidates(proxy.CandidatesConfig{CandidateMaxInFlight: 1})

	assert.False(t, candidates.QuarantineStatus().Ok())
	select {
	case <-candidates.Quarantined():
		t.Fatal("healthy candidates reported quarantine")
	default:
	}

	gap := model.QuarantineReason{Kind: model.QuarantineUnable, Detail: "schema gap"}
	candidates.Quarantine(context.Background(), gap)
	// Only the first reason is kept.
	candidates.Quarantine(context.Background(), model.QuarantineReason{Kind: model.QuarantineCapacity, Detail: "later"})

	delivered := <-candidates.Quarantined()
	status, quarantined := candidates.QuarantineStatus().Get()
	assert.True(t, quarantined)
	assert.Equal(t, model.Quarantine{Reason: gap}, status, assert.Exclude[time.Time]())
	assert.False(t, status.Since.IsZero())
	assert.Equal(t, status, delivered)
	select {
	case <-candidates.Quarantined():
		t.Fatal("a later reason was delivered")
	default:
	}
}

func TestCandidatesConcurrencyLimitQuarantines(t *testing.T) {
	candidates := proxy.NewCandidates(proxy.CandidatesConfig{CandidateMaxInFlight: 1})

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	assert.True(t, candidates.Start(context.Background(), cancel).Ok())
	assert.False(t, candidates.Start(context.Background(), cancel).Ok())

	status, quarantined := candidates.QuarantineStatus().Get()
	assert.True(t, quarantined)
	assert.Equal(t, model.QuarantineReason{Kind: model.QuarantineCapacity, Detail: "candidate concurrency limit exceeded"}, status.Reason)
}

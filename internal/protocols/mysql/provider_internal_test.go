package mysql

import (
	"context"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/mysql/scripted"
	"github.com/block/spectre/internal/state"
)

func TestProviderKindsAreEgressOnly(t *testing.T) {
	assert.Equal(t, []state.Kind{{
		Direction:   model.Egress,
		Protocol:    scripted.Protocol,
		Description: "Module that sends statements to a MySQL database.",
	}}, NewProvider().Kinds())
}

func TestProviderListens(t *testing.T) {
	enabled := NewConfig()
	enabled.Upstream = "db.example:3306"
	enabled.ReferenceListen = "unix:/sockets/reference.sock"
	enabled.CandidateListen = "unix:/sockets/candidate.sock"
	for name, test := range map[string]struct {
		config   Config
		expected []protocols.Listen
	}{
		"Disabled": {config: NewConfig()},
		"Enabled": {config: enabled, expected: []protocols.Listen{
			{Name: "reference", Address: "unix:/sockets/reference.sock", Transport: protocols.Stream},
			{Name: "candidate", Address: "unix:/sockets/candidate.sock", Transport: protocols.Stream},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.expected, NewProvider().Listens(test.config))
		})
	}
}

func TestProviderNewNeedsAnEgressSide(t *testing.T) {
	_, err := NewProvider().New(t.Context(), NewConfig(), protocols.Runtime{})
	assert.EqualError(t, err, "MySQL needs an egress side")
}

// A mirror that does not proxy MySQL must not stay unready waiting for it.
func TestIdleServerMarksEgressReadyAndWaitsForTheContext(t *testing.T) {
	egress := health.New(nil)
	runtime := protocols.Runtime{Egress: Some(protocols.Side{Health: egress})}
	server, err := NewProvider().New(t.Context(), NewConfig(), runtime)
	assert.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan error, 1)

	go func() { stopped <- server.Serve(ctx, protocols.Listeners{}) }()

	deadline := time.Now().Add(5 * time.Second)
	for !egress.Ready() {
		assert.True(t, time.Now().Before(deadline), "egress did not become ready")
		time.Sleep(time.Millisecond)
	}
	select {
	case <-stopped:
		t.Fatal("the idle server returned before its context ended")
	default:
	}
	cancel()
	assert.NoError(t, <-stopped)
}

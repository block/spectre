// Package server is the SPECTRE server, which collects the heartbeats that
// mirrors send. It keeps their latest states in memory or in Redis.
package server

import (
	"time"

	"github.com/block/spectre/internal/server/storage"
)

// ServeConfig configures the SPECTRE server.
type ServeConfig struct {
	Listen   string         `default:"127.0.0.1:50070" help:"Address for the SPECTRE server: host:port, or unix:<path|@abstract>."`
	Storage  storage.Config `embed:""`
	StateTTL time.Duration  `default:"90s" help:"How long a mirror's state is listed after its last heartbeat. Keep it above the heartbeat interval."`
}

// Package server is the SPECTRE server, which collects the states that mirrors
// stream. It keeps their states and divergences in memory or in Redis.
package server

import (
	"time"

	"github.com/block/spectre/internal/server/storage"
)

// ServeConfig configures the SPECTRE server.
type ServeConfig struct {
	Listen   string         `default:"127.0.0.1:50070" help:"Address for the SPECTRE server: host:port, or unix:<path|@abstract>."`
	Storage  storage.Config `embed:""`
	StateTTL time.Duration  `default:"90s" help:"How long a mirror's state is listed after its last update. Keep it above the mirrors' keepalive interval."`
}

// ClearConfig names the service whose divergences to clear, and optionally one
// of its environments.
type ClearConfig struct {
	Service     string `required:"" help:"Service whose divergences to clear."`
	Environment string `help:"Only clear the divergences in this environment. Without it, clear every environment of the service."`
}

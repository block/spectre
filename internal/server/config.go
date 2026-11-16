// Package server is the SPECTRE server, which collects the states that mirrors
// stream. It keeps their states and divergences in memory or in Redis.
package server

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/server/storage"
)

// ServeConfig configures the SPECTRE server.
type ServeConfig struct {
	Listen   string         `default:"127.0.0.1:50070" help:"Address for the SPECTRE server: host:port, or unix:<path|@abstract>."`
	Storage  storage.Config `embed:""`
	StateTTL time.Duration  `default:"90s" help:"How long a mirror's state is listed after its last update. Keep it above the mirrors' keepalive interval."`
}

// NewServeConfig returns the default server configuration.
func NewServeConfig() ServeConfig {
	config := ServeConfig{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply server defaults"))
	}
	return config
}

// ClearConfig names the service environment whose divergences to clear.
type ClearConfig struct {
	Service     string `required:"" help:"Service whose divergences to clear."`
	Environment string `required:"" help:"Environment whose divergences to clear."`
}

// Package server is the SPECTRE server, which collects the quarantines that
// mirrors report. It keeps them in a storage backend, in memory or in Redis.
package server

import (
	"github.com/block/spectre/internal/server/storage"
)

// ServeConfig configures the SPECTRE server.
type ServeConfig struct {
	Listen  string         `default:"127.0.0.1:50070" help:"Address for the SPECTRE server: host:port, or unix:<path|@abstract>."`
	Storage storage.Config `embed:""`
}

// ClearConfig names the service environment whose divergences to clear.
type ClearConfig struct {
	Service     string `required:"" help:"Service whose divergences to clear."`
	Environment string `required:"" help:"Environment whose divergences to clear."`
}

// Package server is the SPECTRE server, which collects the quarantines that
// mirrors report. It keeps them in memory, so a restart loses them.
package server

import "github.com/block/spectre/internal/server/client"

// ReporterConfig configures how a mirror reports quarantines to the SPECTRE server.
type ReporterConfig struct {
	Client      client.Config `embed:""`
	Service     string        `required:"" help:"Service the mirror runs, reported to the SPECTRE server."`
	Environment string        `required:"" help:"Environment the mirror runs in, such as production-us-west-2, reported to the SPECTRE server."`
	Revision    string        `required:"" help:"Commit of the candidate the mirror runs, reported to the SPECTRE server."`
}

// ServeConfig configures the SPECTRE server.
type ServeConfig struct {
	Listen string `default:"127.0.0.1:50070" help:"Address for the SPECTRE server: host:port, or unix:<path|@abstract>."`
}

// ClearConfig names the service environment whose divergences to clear.
type ClearConfig struct {
	Client      client.Config `embed:""`
	Service     string        `required:"" help:"Service whose divergences to clear."`
	Environment string        `required:"" help:"Environment whose divergences to clear."`
}

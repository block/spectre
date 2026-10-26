// Package server is the SPECTRE server, which collects the quarantines that
// mirrors report. It keeps them in memory, so a restart loses them.
package server

// ClientConfig locates the SPECTRE server.
type ClientConfig struct {
	URL string `name:"server-url" required:"" placeholder:"URL" help:"Base URL of the SPECTRE server."`
}

// ReporterConfig configures how a mirror reports quarantines to the SPECTRE server.
type ReporterConfig struct {
	Client      ClientConfig `embed:""`
	Service     string       `required:"" help:"Service the mirror runs, reported to the SPECTRE server."`
	Environment string       `required:"" help:"Environment the mirror runs in, such as production-us-west-2, reported to the SPECTRE server."`
	Revision    string       `required:"" help:"Commit of the candidate the mirror runs, reported to the SPECTRE server."`
}

// ServeConfig configures the SPECTRE server.
type ServeConfig struct {
	Listen string `default:"127.0.0.1:50070" help:"Address for the SPECTRE server: host:port, or unix:<path|@abstract>."`
}

// QueryConfig configures a command that queries the SPECTRE server.
type QueryConfig struct {
	Client ClientConfig `embed:""`
	JSON   bool         `help:"Print JSON instead of text."`
}

// DivergencesConfig selects the divergences that the divergences command lists.
type DivergencesConfig struct {
	Query       QueryConfig `embed:""`
	Service     string      `help:"Only list divergences of this service."`
	Environment string      `help:"Only list divergences in this environment."`
	Revision    string      `help:"Only list divergences of this candidate revision."`
	Node        string      `help:"Only list divergences on routes of this migration state node."`
}

// ClearConfig names the service environment whose divergences to clear.
type ClearConfig struct {
	Client      ClientConfig `embed:""`
	Service     string       `required:"" help:"Service whose divergences to clear."`
	Environment string       `required:"" help:"Environment whose divergences to clear."`
}

// Package coordinator groups the quarantines that mirrors report. It keeps them in
// memory, because mirrors resend their whole state on every heartbeat.
package coordinator

import (
	"time"

	"github.com/alecthomas/errors"
)

// ClientConfig locates the coordination service.
type ClientConfig struct {
	URL string `name:"coordinator-url" env:"SPECTRE_COORDINATOR_URL" placeholder:"URL" help:"Base URL of the SPECTRE coordination service."`
}

// ReporterConfig configures how a mirror reports its state to the coordination service.
type ReporterConfig struct {
	Client      ClientConfig `embed:""`
	Service     string       `env:"SPECTRE_SERVICE" help:"Service the mirror runs, reported to the coordination service."`
	Environment string       `env:"SPECTRE_ENVIRONMENT" help:"Environment the mirror runs in, such as production-us-west-2, reported to the coordination service."`
	Revision    string       `env:"SPECTRE_REVISION" help:"Commit of the candidate the mirror runs, reported to the coordination service."`
	// Heartbeat is how often the mirror reports while nothing changes.
	Heartbeat time.Duration `name:"coordinator-heartbeat" default:"30s" help:"How often the mirror reports its state to the coordination service."`
}

// Enabled reports whether the mirror reports to a coordination service.
func (c ReporterConfig) Enabled() bool {
	return c.Client.URL != ""
}

// Validate requires the mirror's identity whenever reporting is enabled.
func (c ReporterConfig) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if c.Service == "" || c.Environment == "" || c.Revision == "" {
		return errors.New("a mirror reporting to the coordination service needs its service, environment, and revision")
	}
	if c.Heartbeat <= 0 {
		return errors.New("coordinator heartbeat must be positive")
	}
	return nil
}

// ServeConfig configures the coordination service.
type ServeConfig struct {
	Listen string `default:"127.0.0.1:50070" help:"Address for the coordination service: host:port, or unix:<path|@abstract>."`
}

// QueryConfig configures a command that queries the coordination service.
type QueryConfig struct {
	Client ClientConfig `embed:""`
	JSON   bool         `help:"Print JSON instead of text."`
}

// Validate requires the coordination service's URL.
func (c QueryConfig) Validate() error {
	if c.Client.URL == "" {
		return errors.New("the coordination service URL is required")
	}
	return nil
}

// DivergencesConfig selects the divergences that the divergences command lists.
type DivergencesConfig struct {
	Query       QueryConfig `embed:""`
	Service     string      `help:"Only list divergences of this service."`
	Environment string      `help:"Only list divergences in this environment."`
	Revision    string      `help:"Only list divergences of this candidate revision."`
	Node        string      `help:"Only list divergences on routes of this migration state node."`
}

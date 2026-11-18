// Package mysqlserver serves MySQL egress points for the protocol engine. Reference
// statements reach the database. Candidate statements never do: each is answered
// from the rows that equivalent reference statements read.
//
// Statements pair by lookup, not by text. A lookup is one read of a table's rows
// by equality, so a join and the separate statements it replaces pair with each other.
package mysqlserver

import (
	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/netaddr"
)

// Config contains the command-line configuration for the MySQL proxy.
type Config struct {
	// Upstream is the database that reference statements reach. MySQL is proxied only when it is set.
	Upstream string `placeholder:"HOST:PORT" help:"MySQL server that reference statements reach. Setting it turns the MySQL proxy on."`
	// User and Password are what the services connect to the proxy with, and the proxy to the database.
	User     string `help:"MySQL user that the services connect as, and that the proxy connects to the MySQL server as."`
	Password string `help:"Password of the MySQL user."`
	// ReferenceListen receives reference statements, which are always forwarded.
	ReferenceListen string `default:"127.0.0.1:50070" help:"Address the reference sends its MySQL statements to: host:port, or unix:<path|@abstract>."`
	// CandidateListen receives candidate statements. It must stay local: a loopback IP or a unix socket.
	CandidateListen string `default:"127.0.0.1:50071" help:"Address the candidate sends its MySQL statements to, on a loopback IP or unix socket: host:port, or unix:<path|@abstract>."`
	// SavedRowsBytes limits the reference rows saved for candidate statements across all statements.
	SavedRowsBytes int `default:"33554432" help:"Maximum bytes of reference rows saved for candidate MySQL statements. Beyond it the candidate is quarantined."`
}

// NewConfig returns the default MySQL proxy configuration, which proxies nothing.
func NewConfig() Config {
	config := Config{}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply MySQL defaults"))
	}
	return config
}

// Enabled reports whether MySQL is proxied.
func (c Config) Enabled() bool {
	return c.Upstream != ""
}

// Validate checks that an enabled MySQL proxy has a user, a local candidate listener and room to save rows.
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if c.User == "" {
		return errors.New("MySQL user is required")
	}
	if c.SavedRowsBytes <= 0 {
		return errors.New("MySQL saved rows size must be positive")
	}
	candidate := netaddr.ParseListen(c.CandidateListen)
	if !candidate.IsUnix() && !candidate.IsLoopback() {
		return errors.New("MySQL candidate listener must use a loopback IP address or a unix socket")
	}
	return nil
}

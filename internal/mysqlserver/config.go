// Package mysqlserver serves MySQL egress points for the protocol engine. Reference
// commands reach the database. Candidate commands never do: each is answered from
// the replies of reference commands that a normaliser pairs it with.
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
	// UpstreamCA turns on TLS to the database, and verifies its certificate chain against these authorities.
	UpstreamCA string `name:"upstream-ca" placeholder:"FILE" help:"PEM certificate authorities of the MySQL server. Setting it connects with TLS and verifies the certificate chain, not the host name."`
	// UpstreamUser and UpstreamPasswordFile are the proxy's credentials, unless UpstreamSecret holds them.
	UpstreamUser         string `name:"upstream-user" placeholder:"USER" help:"User the proxy connects to the MySQL server as, with the password in --mysql-upstream-password-file."`
	UpstreamPasswordFile string `name:"upstream-password-file" placeholder:"FILE" help:"File holding the password of --mysql-upstream-user. It is read for every connection, so it can be rotated."`
	// UpstreamSecret holds the proxy's credentials as the JSON that AWS Secrets Manager keeps for databases.
	UpstreamSecret string `name:"upstream-secret" placeholder:"ID" help:"AWS Secrets Manager secret whose JSON username and password the proxy connects to the MySQL server with. It is read again when the server refuses them."`
	// ReferenceListen receives reference statements, which are always forwarded. It must stay local, because
	// it accepts any credentials and connects with the proxy's own.
	ReferenceListen string `default:"127.0.0.1:50070" help:"Address the reference sends its MySQL statements to, on a loopback IP or unix socket: host:port, or unix:<path|@abstract>."`
	// CandidateListen receives candidate statements. It must stay local: a loopback IP or a unix socket.
	CandidateListen string `default:"127.0.0.1:50071" help:"Address the candidate sends its MySQL statements to, on a loopback IP or unix socket: host:port, or unix:<path|@abstract>."`
	// SavedRepliesBytes limits the reference replies saved for candidate statements across all statements.
	SavedRepliesBytes int `default:"33554432" help:"Maximum bytes of reference replies saved for candidate MySQL statements. Beyond it the candidate is quarantined."`
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

// Validate checks that an enabled MySQL proxy has one source of credentials, local
// listeners and room to save replies.
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if (c.UpstreamSecret == "") == (c.UpstreamUser == "") {
		return errors.New("MySQL proxy needs either an upstream user or an upstream secret")
	}
	if (c.UpstreamUser == "") != (c.UpstreamPasswordFile == "") {
		return errors.New("MySQL upstream user and upstream password file must be set together")
	}
	if c.SavedRepliesBytes <= 0 {
		return errors.New("MySQL saved replies size must be positive")
	}
	if !isLocal(c.ReferenceListen) {
		return errors.New("MySQL reference listener must use a loopback IP address or a unix socket")
	}
	if !isLocal(c.CandidateListen) {
		return errors.New("MySQL candidate listener must use a loopback IP address or a unix socket")
	}
	return nil
}

// isLocal reports whether listen accepts only connections from this host.
func isLocal(listen string) bool {
	address := netaddr.ParseListen(listen)
	return address.IsUnix() || address.IsLoopback()
}

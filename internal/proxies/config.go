// Package proxies runs the SPECTRE proxies in front of a reference and a candidate
// service, and streams the mirror's state to the SPECTRE server. It never starts
// the services itself.
package proxies

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/reporter"
)

// Config contains the command-line configuration of the proxies and reporting.
type Config struct {
	// Reflection loads and compares both backends' descriptors via gRPC reflection.
	Reflection bool `name:"reflection" negatable:"" default:"true" help:"Load backend descriptors with gRPC reflection and require them to match; disable to compare using only the static descriptors. Ingress is ready only once both backends accept."`
	// Destinations maps each request host to its upstream URL for egress.
	Destinations map[string]string `name:"destination" placeholder:"HOST=URL" help:"Upstream URL for requests to HOST, passed to the egress proxy. HOST may be a glob, such as *.example. Repeat for each dependency."`
	// DefaultDestination receives reference requests to hosts no destination matches.
	DefaultDestination string `placeholder:"URL" help:"Upstream URL for reference requests to hosts no destination matches, passed to the egress proxy. Without it, they go to the host as received over plaintext HTTP."`
	// ScriptsDir holds the comparison scripts passed to both proxies.
	ScriptsDir string `type:"existingdir" placeholder:"DIR" help:"Directory of comparison scripts passed to both proxies."`
	// StateFile is the migration state that decides which routes ingress mirrors.
	StateFile string `type:"existingfile" placeholder:"FILE" help:"Migration state TOML file. Ingress mirrors only routes of migrated nodes, and quarantines the candidate on a route no node lists. Without it, every request is mirrored."`
	// SchemaDir holds the TypeScript schema passed to both proxies.
	SchemaDir string `type:"existingdir" placeholder:"DIR" help:"Directory of TypeScript schema passed to both proxies."`
	// MatchWindow bounds how long a candidate call waits for a matching reference call.
	MatchWindow time.Duration `default:"5s" help:"Maximum time a candidate egress call waits for an equivalent reference call before a 502."`
	// CandidateStartupTimeout bounds how long the candidate has to accept a first connection.
	CandidateStartupTimeout time.Duration `default:"5m" help:"Maximum time for the candidate to accept a first connection before it is quarantined. Ingress sends it nothing until then."`
	// Descriptors supplies protobuf descriptors to both proxies.
	Descriptors descriptors.Config `embed:""`
	// Server reports the mirror's state to the SPECTRE server.
	Server reporter.Config `embed:""`
}

// NewConfig returns the default mirror configuration.
func NewConfig() Config {
	// ApplyDefaults validates required fields, so seed them while applying tag defaults.
	const placeholder = "placeholder"
	config := Config{Server: reporter.Config{
		Client:      client.Config{URL: placeholder},
		Service:     placeholder,
		Environment: placeholder,
		Revision:    placeholder,
	}}
	// The server URL's default interpolates a variable that only spectre.Main sets.
	if err := kong.ApplyDefaults(&config, kong.Vars{"server_url": ""}); err != nil {
		panic(errors.Wrap(err, "apply mirror defaults"))
	}
	config.Server.Client = client.Config{}
	config.Server.Service = ""
	config.Server.Environment = ""
	config.Server.Revision = ""
	return config
}

// Validate checks that the timeouts and reporting intervals are usable.
func (c Config) Validate() error {
	if c.MatchWindow <= 0 {
		return errors.New("match window must be positive")
	}
	if c.CandidateStartupTimeout <= 0 {
		return errors.New("candidate startup timeout must be positive")
	}
	return errors.WithStack(c.Server.Validate())
}

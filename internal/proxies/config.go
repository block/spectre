// Package proxies runs the SPECTRE proxies in front of a reference and a candidate
// service, and streams the mirror's state to the SPECTRE server. It never starts
// the services itself.
package proxies

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/reporter"
)

// Config contains the command-line configuration that the proxies share: scripts,
// migration state, candidate admission, health and reporting. Providers own the rest.
type Config struct {
	// ScriptsDir holds the comparison scripts passed to every protocol.
	ScriptsDir string `type:"existingdir" placeholder:"DIR" help:"Directory of comparison scripts passed to both proxies."`
	// ScriptVariables are the values the comparison scripts read with variable.
	ScriptVariables map[string]string `name:"script-variable" placeholder:"NAME=VALUE" help:"Value the comparison scripts read with variable(NAME), such as an environment's host name. Repeat for each variable."`
	// StateFile is the migration state that decides which routes ingress mirrors.
	StateFile string `type:"existingfile" placeholder:"FILE" help:"Migration state TOML file. Ingress mirrors only routes of migrated nodes, and quarantines the candidate on a route no node lists. Without it, every request is mirrored."`
	// SchemaDir holds the TypeScript schema passed to every protocol.
	SchemaDir string `type:"existingdir" placeholder:"DIR" help:"Directory of TypeScript schema passed to both proxies."`
	// MatchWindow bounds how long a candidate call waits for a matching reference call.
	MatchWindow time.Duration `default:"5s" help:"Maximum time a candidate egress call waits for an equivalent reference call before a 502."`
	// Candidates limits candidate admission in each direction.
	Candidates proxy.CandidatesConfig `embed:""`
	// EgressHealthListen serves the egress proxies' own health endpoints.
	EgressHealthListen string `default:"127.0.0.1:50062" help:"Address of the egress proxies' own /livez and /readyz, which cover every protocol's egress: host:port, or unix:<path|@abstract>."`
	// HealthListen serves the health of every proxy, for probes.
	HealthListen string `default:"127.0.0.1:50063" help:"Address of /livez, and of /readyz once all proxies are ready, for probes such as Kubernetes': host:port, or unix:<path|@abstract>. Use 0.0.0.0:<port> to probe it from outside the host."`
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

// Validate checks that the timeouts, candidate admission and the reporting intervals are usable.
func (c Config) Validate() error {
	if c.MatchWindow <= 0 {
		return errors.New("match window must be positive")
	}
	if err := c.Candidates.Validate(); err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(c.Server.Validate())
}

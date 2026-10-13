// Package supervisor runs a candidate and reference service behind the SPECTRE
// proxies, sandboxing the candidate's egress and splitting host resources with cgroups.
package supervisor

import (
	"time"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal/descriptors"
)

// Config contains the command-line configuration for the supervisor.
type Config struct {
	// Candidate is the shell command for the sandboxed candidate service.
	Candidate string `required:"" help:"Shell command for the candidate service. $SPECTRE_LISTEN and $SPECTRE_EGRESS are exported to it."`
	// Reference is the shell command for the authoritative reference service.
	Reference string `required:"" help:"Shell command for the reference service. $SPECTRE_LISTEN and $SPECTRE_EGRESS are exported to it."`
	// Listen is the public ingress address that clients send traffic to.
	Listen string `default:"127.0.0.1:50050" help:"Public ingress address: host:port, or unix:<path|@abstract> for a unix socket."`
	// SocketDir holds the unix sockets wiring the proxies to the services.
	SocketDir string `default:"dist/sockets" help:"Directory for the unix sockets wiring the proxies to the services."`
	// EgressBin is the egress proxy executable, on PATH or an absolute path.
	EgressBin string `default:"spectre-egress" help:"Egress proxy executable, found on PATH or given as an absolute path."`
	// IngressBin is the ingress proxy executable, on PATH or an absolute path.
	IngressBin string `default:"spectre-ingress" help:"Ingress proxy executable, found on PATH or given as an absolute path."`
	// Destinations maps each request host to its upstream URL for egress.
	Destinations map[string]string `name:"destination" placeholder:"HOST=URL" help:"Upstream URL for requests to HOST, passed to the egress proxy. Repeat for each dependency."`
	// ScriptsDir holds the comparison scripts passed to both proxies.
	ScriptsDir string `type:"existingdir" placeholder:"DIR" help:"Directory of comparison scripts passed to both proxies."`
	// SchemaDir holds the TypeScript schema passed to both proxies.
	SchemaDir string `type:"existingdir" placeholder:"DIR" help:"Directory of TypeScript schema passed to both proxies."`
	// Descriptors supplies protobuf descriptors to both proxies.
	Descriptors descriptors.Config `embed:""`
	// Limits configures the cgroup resource split between the services.
	Limits LimitConfig `embed:""`
	// Sandbox configures the candidate egress sandbox.
	Sandbox SandboxConfig `embed:""`
	// StartupTimeout bounds how long each process has to start listening.
	StartupTimeout time.Duration `default:"30s" help:"Maximum time for each process to begin accepting connections."`
	// ShutdownTimeout bounds graceful shutdown before processes are killed.
	ShutdownTimeout time.Duration `default:"10s" help:"Maximum time for processes to exit after a termination signal."`
}

// LimitConfig contains the cgroup v2 resource split between the services.
type LimitConfig struct {
	// Enabled applies the cgroup split; disable it where cgroups are unavailable.
	Enabled bool `name:"limits" negatable:"" default:"true" help:"Apply a cgroup v2 CPU and memory split to the services."`
	// Parent is the cgroup v2 path hosting the service cgroups.
	Parent string `name:"cgroup-parent" placeholder:"PATH" help:"cgroup v2 path to host the service cgroups under, relative to the cgroup mount. Defaults to the supervisor's own cgroup."`
	// MemoryMax is the total memory in bytes split between the services.
	MemoryMax int64 `name:"memory-max" default:"0" help:"Total memory in bytes split between the services. 0 detects the system memory."`
	// CandidateCPUWeight is the candidate cpu.weight, 1-10000.
	CandidateCPUWeight uint `name:"candidate-cpu-weight" default:"100" help:"Candidate cpu.weight (1-10000); its ratio to the reference weight splits CPU under contention."`
	// ReferenceCPUWeight is the reference cpu.weight, 1-10000.
	ReferenceCPUWeight uint `name:"reference-cpu-weight" default:"100" help:"Reference cpu.weight (1-10000)."`
	// CandidateMemoryShare is the candidate share of the memory budget.
	CandidateMemoryShare uint `name:"candidate-memory-share" default:"1" help:"Candidate share of the memory budget, relative to the reference share."`
	// ReferenceMemoryShare is the reference share of the memory budget.
	ReferenceMemoryShare uint `name:"reference-memory-share" default:"1" help:"Reference share of the memory budget."`
}

// SandboxConfig contains the candidate egress sandbox configuration.
type SandboxConfig struct {
	// EgressEnabled blocks the candidate from opening IP sockets with seccomp,
	// so it can only reach dependencies through the egress proxy.
	EgressEnabled bool `name:"seccomp-egress" negatable:"" default:"true" help:"Block the candidate from opening IPv4 and IPv6 sockets with a seccomp filter, leaving only the egress proxy's unix socket."`
}

// NewConfig returns the default supervisor configuration.
func NewConfig() Config {
	// ApplyDefaults validates required fields, so seed them while applying tag defaults.
	config := Config{Candidate: "placeholder", Reference: "placeholder"}
	if err := kong.ApplyDefaults(&config); err != nil {
		panic(errors.Wrap(err, "apply supervisor defaults"))
	}
	config.Candidate = ""
	config.Reference = ""
	return config
}

// Validate checks that the ratios and timeouts are usable. The candidate and
// reference commands are enforced as required flags by Kong.
func (c Config) Validate() error {
	if c.SocketDir == "" {
		return errors.New("socket directory must be set")
	}
	if c.StartupTimeout <= 0 || c.ShutdownTimeout <= 0 {
		return errors.New("startup and shutdown timeouts must be positive")
	}
	return errors.WithStack(c.Limits.Validate())
}

// Validate checks that the cgroup weights and shares are usable.
func (c LimitConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	for _, weight := range []uint{c.CandidateCPUWeight, c.ReferenceCPUWeight} {
		if weight < 1 || weight > 10000 {
			return errors.New("cpu weights must be between 1 and 10000")
		}
	}
	if c.CandidateMemoryShare+c.ReferenceMemoryShare == 0 {
		return errors.New("memory shares cannot both be zero")
	}
	if c.MemoryMax < 0 {
		return errors.New("memory max cannot be negative")
	}
	return nil
}

// memorySplit divides budget between the candidate and reference by their shares.
// A zero budget leaves both limits unset.
func (c LimitConfig) memorySplit(budget int64) (candidate int64, reference int64) {
	candidateShare := int64(c.CandidateMemoryShare)         //nolint:gosec // operator-set share, small by construction
	total := candidateShare + int64(c.ReferenceMemoryShare) //nolint:gosec // operator-set share, small by construction
	if budget <= 0 || total == 0 {
		return 0, 0
	}
	candidate = budget * candidateShare / total
	return candidate, budget - candidate
}

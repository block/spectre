package supervisor

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/mirror"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/state"
)

// roles name the service leaves and their wiring.
const (
	roleReference = "reference"
	roleCandidate = "candidate"
)

// Supervisor runs the candidate and reference services behind the SPECTRE
// proxies, with the candidate's egress sandboxed and host resources split.
type Supervisor struct {
	config  Config
	log     *slog.Logger
	selfExe string
	mirror  *mirror.Mirror

	egressReference string
	egressCandidate string
	egressHealth    string
	referenceSock   string
	candidateSock   string

	limits  Option[*cgroups]
	running int
	exits   chan exit
}

// exit reports a finished component.
type exit struct {
	name string
	err  error
}

// New validates config and returns a supervisor that logs to ctx's logger. The
// catalog checks the migration state, if config names one.
func New(ctx context.Context, config Config, catalog *state.Catalog) (*Supervisor, error) {
	if config.Candidate == "" || config.Reference == "" {
		return nil, errors.New("candidate and reference commands are required")
	}
	if err := config.Validate(); err != nil {
		return nil, errors.WithStack(err)
	}
	selfExe, err := os.Executable()
	if err != nil {
		return nil, errors.Wrap(err, "find supervisor executable")
	}
	s := &Supervisor{
		config:  config,
		log:     logger.FromContext(ctx).With(logger.SubsystemKey, "supervisor"),
		selfExe: selfExe,
		limits:  None[*cgroups](),
		exits:   make(chan exit, 3),
	}
	if err := s.prepareWiring(); err != nil {
		return nil, err
	}
	s.mirror, err = mirror.New(ctx, config.Mirror, s.wiring(), catalog)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return s, nil
}

// Run starts the proxies and services, then runs until the context is cancelled
// or a component exits, after which it stops the rest.
func (s *Supervisor) Run(ctx context.Context) error {
	if err := s.setupLimits(ctx); err != nil {
		return err
	}
	defer s.cleanupLimits(ctx)

	// Cancelling runCtx stops the proxies and terminates the services.
	runCtx, cancel := context.WithCancel(ctx)
	defer s.drain()
	defer cancel()

	if err := s.startAll(runCtx); err != nil {
		return err
	}
	s.log.InfoContext(ctx, "Supervisor ready", "ingress", s.config.Listen, "egress_sandbox", s.sandboxActive(), "limits", s.limits.Ok())
	return s.waitForExit(ctx)
}

// prepareWiring resolves the socket directory and the socket topology.
func (s *Supervisor) prepareWiring() error {
	socketDir, err := filepath.Abs(s.config.SocketDir)
	if err != nil {
		return errors.Wrap(err, "resolve socket directory")
	}
	if err := os.MkdirAll(socketDir, 0o750); err != nil {
		return errors.Wrap(err, "create socket directory")
	}
	s.egressReference = filepath.Join(socketDir, "egress-reference.sock")
	s.egressCandidate = filepath.Join(socketDir, "egress-candidate.sock")
	s.egressHealth = filepath.Join(socketDir, "egress-health.sock")
	s.referenceSock = filepath.Join(socketDir, "reference.sock")
	s.candidateSock = filepath.Join(socketDir, "candidate.sock")
	return nil
}

// sandboxActive reports whether the candidate egress sandbox will be applied.
func (s *Supervisor) sandboxActive() bool {
	return s.config.SeccompEgress && egressSandboxSupported()
}

// setupLimits prepares the cgroup leaves when limits are enabled and supported.
func (s *Supervisor) setupLimits(ctx context.Context) error {
	limit := s.config.Limits
	if !limit.Enabled {
		return nil
	}
	if !cgroupsSupported() {
		return errors.New("cgroup v2 is not available; rerun with --no-limits to run without resource splits")
	}
	base, err := cgroupBaseDir(limit.Parent)
	if err != nil {
		return err
	}
	control, err := newCgroups(base, os.Getpid())
	if err != nil {
		return err
	}
	budget := limit.MemoryMax
	if budget == 0 {
		if budget, err = systemMemoryBytes(); err != nil {
			return err
		}
	}
	candidateMemory, referenceMemory := limit.memorySplit(budget)
	if err := control.limit(roleCandidate, limit.CandidateCPUWeight, candidateMemory); err != nil {
		return err
	}
	if err := control.limit(roleReference, limit.ReferenceCPUWeight, referenceMemory); err != nil {
		return err
	}
	s.limits = Some(control)
	s.log.InfoContext(ctx, "Prepared cgroup split", "base", base, "candidate_memory", candidateMemory, "reference_memory", referenceMemory)
	return nil
}

// cleanupLimits removes the service cgroups once the services have stopped.
func (s *Supervisor) cleanupLimits(ctx context.Context) {
	control, ok := s.limits.Get()
	if !ok {
		return
	}
	if err := control.cleanup(); err != nil {
		s.log.WarnContext(ctx, "Failed to clean up cgroups", "error", err)
	}
}

// startAll brings up the proxies, then each service in turn, waiting for each to
// accept connections before the next.
func (s *Supervisor) startAll(ctx context.Context) error {
	if err := s.mirror.Start(ctx); err != nil {
		return errors.WithStack(err)
	}
	s.running++
	go func() { s.exits <- exit{name: "mirror", err: s.mirror.Wait(ctx)} }()
	reference := s.serviceProcess(ctx, roleReference, httpUnix(s.egressReference), unixListen(s.referenceSock), false)
	if err := s.launchService(ctx, reference, s.referenceSock); err != nil {
		return err
	}
	candidate := s.serviceProcess(ctx, roleCandidate, httpUnix(s.egressCandidate), unixListen(s.candidateSock), s.sandboxActive())
	if err := s.launchService(ctx, candidate, s.candidateSock); err != nil {
		return err
	}
	if err := s.place(reference.pid(), roleReference); err != nil {
		return err
	}
	return s.place(candidate.pid(), roleCandidate)
}

// launchService starts a service process, watches for its exit, and waits until
// it listens.
func (s *Supervisor) launchService(ctx context.Context, process *managedProcess, socket string) error {
	if err := process.start(); err != nil {
		return err
	}
	s.running++
	go func() { s.exits <- exit{name: process.Name(), err: process.wait()} }()
	s.log.InfoContext(ctx, "Started process", "name", process.Name(), "pid", process.pid())
	startup, cancel := context.WithTimeout(ctx, s.config.StartupTimeout)
	defer cancel()
	if err := netaddr.ParseListen(unixListen(socket)).AwaitAccepting(startup); err != nil {
		return errors.Wrapf(err, "%s did not start listening", process.Name())
	}
	return nil
}

// place moves a started service's process into its cgroup leaf when limits are enabled.
func (s *Supervisor) place(pid int, role string) error {
	control, ok := s.limits.Get()
	if !ok {
		return nil
	}
	return control.place(role, pid)
}

// waitForExit blocks until the context is cancelled or a component exits.
func (s *Supervisor) waitForExit(ctx context.Context) error {
	select {
	case <-ctx.Done():
		s.log.InfoContext(ctx, "Shutting down after signal")
		return nil
	case finished := <-s.exits:
		s.running--
		s.log.WarnContext(ctx, "Component exited; shutting down", "name", finished.name, "error", finished.err)
		return errors.Wrapf(finished.err, "%s exited", finished.name)
	}
}

// drain waits for the remaining components to exit after the run context is
// cancelled.
func (s *Supervisor) drain() {
	for s.running > 0 {
		<-s.exits
		s.running--
	}
}

// serviceProcess builds a candidate or reference service command, optionally
// re-exec'd through the egress sandbox.
func (s *Supervisor) serviceProcess(ctx context.Context, role string, egressURL string, listen string, sandbox bool) *managedProcess {
	shell := s.config.Reference
	if role == roleCandidate {
		shell = s.config.Candidate
	}
	var cmd *exec.Cmd
	if sandbox {
		cmd = s.command(ctx, s.selfExe, SandboxArg, "/bin/sh", "-c", shell)
	} else {
		cmd = s.command(ctx, "/bin/sh", "-c", shell)
	}
	cmd.Env = append(cmd.Environ(), "SPECTRE_LISTEN="+listen, "SPECTRE_EGRESS="+egressURL)
	// Only stdout carries the service's logs; stderr passes through untouched.
	stdout := newServiceLogWriter(os.Stdout, role)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	return newManagedProcess(role, cmd, stdout)
}

// command builds a child command in its own process group. Context cancellation
// terminates the whole group, escalating to a kill after the shutdown timeout.
func (s *Supervisor) command(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...) //nolint:gosec // G204: the supervisor runs operator-provided service commands by design
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return signalGroup(cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = s.config.ShutdownTimeout
	return cmd
}

// wiring connects the proxies to the services through the socket directory.
func (s *Supervisor) wiring() mirror.Wiring {
	return mirror.Wiring{
		Listen:          s.config.Listen,
		Reference:       backendURL(s.config.ReferenceProtocol, s.referenceSock),
		Candidate:       backendURL(s.config.CandidateProtocol, s.candidateSock),
		ReferenceEgress: unixListen(s.egressReference),
		CandidateEgress: unixListen(s.egressCandidate),
		EgressHealth:    unixListen(s.egressHealth),
		Health:          None[string](),
	}
}

func unixListen(path string) string { return "unix:" + path }
func httpUnix(path string) string   { return "http+unix:" + path }
func h2cUnix(path string) string    { return "h2c+unix:" + path }

// protocolHTTP selects HTTP/1.1 for a leg; any other value selects h2c.
const protocolHTTP = "http"

// backendURL builds the ingress upstream URL for a leg from its protocol: HTTP/1.1
// over a unix socket, or unencrypted HTTP/2 (h2c) for everything else.
func backendURL(protocol string, path string) string {
	if protocol == protocolHTTP {
		return httpUnix(path)
	}
	return h2cUnix(path)
}

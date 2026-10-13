package supervisor

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
)

// roles name the service leaves and their wiring.
const (
	roleReference = "reference"
	roleCandidate = "candidate"
)

// Supervisor runs the candidate and reference services behind the SPECTRE
// proxies, with the candidate's egress sandboxed and host resources split.
type Supervisor struct {
	config    Config
	logConfig logger.Config
	log       *slog.Logger
	output    io.Writer
	selfExe   string

	egressReference string
	egressCandidate string
	egressHealth    string
	referenceSock   string
	candidateSock   string
	ingress         *netaddr.Endpoint

	limits  Option[*cgroups]
	started []*managedProcess
	exits   chan exit
}

// exit reports a finished process.
type exit struct {
	process *managedProcess
	err     error
}

// New validates config and returns a supervisor that logs to log and tags child
// output written to output.
func New(config Config, logConfig logger.Config, log *slog.Logger, output io.Writer) (*Supervisor, error) {
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
	return &Supervisor{
		config:    config,
		logConfig: logConfig,
		log:       log,
		output:    output,
		selfExe:   selfExe,
		limits:    None[*cgroups](),
		exits:     make(chan exit, 4),
	}, nil
}

// Run starts the proxies and services, then runs until the context is cancelled
// or a process exits, after which it stops the rest.
func (s *Supervisor) Run(ctx context.Context) error {
	if err := s.prepareWiring(); err != nil {
		return err
	}
	if err := s.setupLimits(ctx); err != nil {
		return err
	}
	defer s.cleanupLimits(ctx)

	// Cancelling runCtx terminates every child via its command cancel handler.
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
	s.ingress = netaddr.ParseListen(s.config.Listen)
	return nil
}

// sandboxActive reports whether the candidate egress sandbox will be applied.
func (s *Supervisor) sandboxActive() bool {
	return s.config.Sandbox.EgressEnabled && egressSandboxSupported()
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

// startAll brings up the proxies and services in dependency order, each waited
// on until it accepts connections.
func (s *Supervisor) startAll(ctx context.Context) error {
	egress := s.egressProcess(ctx)
	if err := s.launch(ctx, egress, "unix", s.egressHealth); err != nil {
		return err
	}
	reference := s.serviceProcess(ctx, roleReference, httpUnix(s.egressReference), unixListen(s.referenceSock), false)
	if err := s.launch(ctx, reference, "unix", s.referenceSock); err != nil {
		return err
	}
	candidate := s.serviceProcess(ctx, roleCandidate, httpUnix(s.egressCandidate), unixListen(s.candidateSock), s.sandboxActive())
	if err := s.launch(ctx, candidate, "unix", s.candidateSock); err != nil {
		return err
	}
	if err := s.place(reference, roleReference); err != nil {
		return err
	}
	if err := s.place(candidate, roleCandidate); err != nil {
		return err
	}
	ingress := s.ingressProcess(ctx)
	return s.launch(ctx, ingress, s.ingress.Network(), s.ingress.Address())
}

// launch starts a process, watches for its exit, and waits until it listens.
func (s *Supervisor) launch(ctx context.Context, process *managedProcess, network string, address string) error {
	if err := process.start(); err != nil {
		return err
	}
	s.started = append(s.started, process)
	go func() { s.exits <- exit{process: process, err: process.wait()} }()
	s.log.InfoContext(ctx, "Started process", "name", process.Name(), "pid", process.pid())
	if err := waitListening(ctx, network, address, s.config.StartupTimeout); err != nil {
		return errors.Wrapf(err, "%s did not start listening", process.Name())
	}
	return nil
}

// place moves a started service into its cgroup leaf when limits are enabled.
func (s *Supervisor) place(process *managedProcess, role string) error {
	control, ok := s.limits.Get()
	if !ok {
		return nil
	}
	return control.place(role, process.pid())
}

// waitForExit blocks until the context is cancelled or a process exits.
func (s *Supervisor) waitForExit(ctx context.Context) error {
	select {
	case <-ctx.Done():
		s.log.InfoContext(ctx, "Shutting down after signal")
		return nil
	case finished := <-s.exits:
		s.started = withoutProcess(s.started, finished.process)
		s.log.WarnContext(ctx, "Process exited; shutting down", "name", finished.process.Name(), "error", finished.err)
		return errors.Wrapf(finished.err, "%s exited", finished.process.Name())
	}
}

// drain waits for the remaining processes to exit after the run context is
// cancelled, which terminates them through their command cancel handlers.
func (s *Supervisor) drain() {
	for len(s.started) > 0 {
		finished := <-s.exits
		s.started = withoutProcess(s.started, finished.process)
	}
}

// egressProcess builds the egress proxy command.
func (s *Supervisor) egressProcess(ctx context.Context) *managedProcess {
	args := []string{
		"--reference-listen=" + unixListen(s.egressReference),
		"--candidate-listen=" + unixListen(s.egressCandidate),
		"--health-listen=" + unixListen(s.egressHealth),
	}
	for _, host := range sortedKeys(s.config.Destinations) {
		args = append(args, "--destination="+host+"="+s.config.Destinations[host])
	}
	args = append(args, s.sharedProxyArgs()...)
	cmd := s.command(ctx, s.config.EgressBin, args...)
	cmd.Env = os.Environ()
	return newManagedProcess("egress", cmd, s.output)
}

// ingressProcess builds the ingress proxy command.
func (s *Supervisor) ingressProcess(ctx context.Context) *managedProcess {
	args := []string{
		"--listen=" + s.config.Listen,
		"--reference=" + h2cUnix(s.referenceSock),
		"--candidate=" + h2cUnix(s.candidateSock),
	}
	args = append(args, s.sharedProxyArgs()...)
	cmd := s.command(ctx, s.config.IngressBin, args...)
	cmd.Env = os.Environ()
	return newManagedProcess("ingress", cmd, s.output)
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
	cmd.Env = append(os.Environ(), "SPECTRE_LISTEN="+listen, "SPECTRE_EGRESS="+egressURL)
	return newManagedProcess(role, cmd, s.output)
}

// command builds a child command that terminates gracefully on context
// cancellation, escalating to a kill after the shutdown timeout.
func (s *Supervisor) command(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...) //nolint:gosec // G204: the supervisor runs operator-provided service and proxy commands by design
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = s.config.ShutdownTimeout
	return cmd
}

// sharedProxyArgs returns the comparison and logging flags passed to both proxies.
func (s *Supervisor) sharedProxyArgs() []string {
	var args []string
	if s.config.ScriptsDir != "" {
		args = append(args, "--scripts-dir="+s.config.ScriptsDir)
	}
	if s.config.SchemaDir != "" {
		args = append(args, "--schema-dir="+s.config.SchemaDir)
	}
	if s.config.Descriptors.DescriptorsDir != "" {
		args = append(args, "--descriptors-dir="+s.config.Descriptors.DescriptorsDir)
	}
	args = append(args, "--log-level="+strings.ToLower(s.logConfig.Level.String()))
	if s.logConfig.JSON {
		args = append(args, "--log-json")
	}
	return args
}

// waitListening polls until the address accepts a connection or the timeout elapses.
func waitListening(ctx context.Context, network string, address string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	dialer := &net.Dialer{Timeout: time.Second}
	for {
		conn, err := dialer.DialContext(ctx, network, address)
		if err == nil {
			return errors.WithStack(conn.Close())
		}
		if ctx.Err() != nil {
			return errors.WithStack(ctx.Err())
		}
		if time.Now().After(deadline) {
			return errors.Wrapf(err, "timed out dialing %s", address)
		}
		select {
		case <-ctx.Done():
			return errors.WithStack(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// withoutProcess drops the already-exited process from the shutdown set.
func withoutProcess(processes []*managedProcess, exited *managedProcess) []*managedProcess {
	remaining := processes[:0]
	for _, process := range processes {
		if process != exited {
			remaining = append(remaining, process)
		}
	}
	return remaining
}

// sortedKeys returns the map keys in a stable order for deterministic arguments.
func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func unixListen(path string) string { return "unix:" + path }
func httpUnix(path string) string   { return "http+unix:" + path }
func h2cUnix(path string) string    { return "h2c+unix:" + path }

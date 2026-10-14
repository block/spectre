package supervisor

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/httpserver"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocol"
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

	limits  Option[*cgroups]
	running int
	exits   chan exit
}

// exit reports a finished component.
type exit struct {
	name string
	err  error
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
		output:    newSyncWriter(output),
		selfExe:   selfExe,
		limits:    None[*cgroups](),
		exits:     make(chan exit, 4),
	}, nil
}

// Run starts the proxies and services, then runs until the context is cancelled
// or a component exits, after which it stops the rest.
func (s *Supervisor) Run(ctx context.Context) error {
	if err := s.prepareWiring(); err != nil {
		return err
	}
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

// startAll brings up the proxies and services in dependency order, waiting for
// each service to accept connections before the next.
func (s *Supervisor) startAll(ctx context.Context) error {
	if err := s.startEgress(ctx); err != nil {
		return err
	}
	reference := s.serviceProcess(ctx, roleReference, httpUnix(s.egressReference), unixListen(s.referenceSock), false)
	if err := s.launchService(ctx, reference, "unix", s.referenceSock); err != nil {
		return err
	}
	candidate := s.serviceProcess(ctx, roleCandidate, httpUnix(s.egressCandidate), unixListen(s.candidateSock), s.sandboxActive())
	if err := s.launchService(ctx, candidate, "unix", s.candidateSock); err != nil {
		return err
	}
	if err := s.place(reference, roleReference); err != nil {
		return err
	}
	if err := s.place(candidate, roleCandidate); err != nil {
		return err
	}
	return s.startIngress(ctx)
}

// startEgress builds the egress proxy, opens its listeners, and serves it.
func (s *Supervisor) startEgress(ctx context.Context) error {
	config := s.egressConfig()
	transport := httpserver.NewTransport(config.Config)
	handler, err := httpserver.NewEgress(ctx, config, s.engineConfig(), transport, s.proxyLogger("egress"))
	if err != nil {
		transport.CloseIdleConnections()
		return errors.Wrap(err, "configure egress")
	}
	listeners, err := listenAll(ctx, config.ReferenceListen, config.CandidateListen, config.HealthListen)
	if err != nil {
		transport.CloseIdleConnections()
		return errors.Wrap(err, "listen for egress")
	}
	s.runProxy(ctx, "egress", transport, func() error {
		return handler.Serve(ctx, listeners[0], listeners[1], listeners[2])
	})
	return nil
}

// startIngress builds the ingress proxy, opens its listener, and serves it.
func (s *Supervisor) startIngress(ctx context.Context) error {
	config := s.ingressConfig()
	transport := httpserver.NewTransport(config.Config)
	loader := Some[httpserver.DescriptorLoader](descriptors.NewReflectionLoader())
	handler, err := httpserver.NewIngress(ctx, config, s.engineConfig(), transport, loader, s.proxyLogger("ingress"))
	if err != nil {
		transport.CloseIdleConnections()
		return errors.Wrap(err, "configure ingress")
	}
	listener, err := netaddr.ParseListen(config.Listen).Listen(ctx)
	if err != nil {
		transport.CloseIdleConnections()
		return errors.Wrap(err, "listen for ingress")
	}
	s.runProxy(ctx, "ingress", transport, func() error {
		return handler.Serve(ctx, listener)
	})
	return nil
}

// runProxy serves a proxy in the background, reporting its exit and releasing the
// transport's idle connections when it stops.
func (s *Supervisor) runProxy(ctx context.Context, name string, transport *httpserver.Transport, serve func() error) {
	s.running++
	go func() {
		defer transport.CloseIdleConnections()
		s.exits <- exit{name: name, err: serve()}
	}()
	s.log.InfoContext(ctx, "Started proxy", "name", name)
}

// launchService starts a service process, watches for its exit, and waits until
// it listens.
func (s *Supervisor) launchService(ctx context.Context, process *managedProcess, network string, address string) error {
	if err := process.start(); err != nil {
		return err
	}
	s.running++
	go func() { s.exits <- exit{name: process.Name(), err: process.wait()} }()
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
	return newManagedProcess(role, cmd, s.output)
}

// command builds a child command that terminates gracefully on context
// cancellation, escalating to a kill after the shutdown timeout.
func (s *Supervisor) command(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...) //nolint:gosec // G204: the supervisor runs operator-provided service commands by design
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = s.config.ShutdownTimeout
	return cmd
}

// engineConfig builds the comparison engine configuration shared by both proxies.
func (s *Supervisor) engineConfig() protocol.Config {
	config := protocol.NewConfig()
	config.Comparison.ScriptsDir = s.config.ScriptsDir
	config.Comparison.Schema.SchemaDir = s.config.SchemaDir
	config.MatchWindow = s.config.MatchWindow
	return config
}

// egressConfig builds the egress proxy configuration from the socket topology.
func (s *Supervisor) egressConfig() httpserver.EgressConfig {
	config := httpserver.NewEgressConfig()
	config.ReferenceListen = unixListen(s.egressReference)
	config.CandidateListen = unixListen(s.egressCandidate)
	config.HealthListen = unixListen(s.egressHealth)
	config.Destinations = s.config.Destinations
	config.Descriptors = s.config.Descriptors
	return config
}

// ingressConfig builds the ingress proxy configuration from the socket topology.
func (s *Supervisor) ingressConfig() httpserver.IngressConfig {
	config := httpserver.NewIngressConfig()
	config.Listen = s.config.Listen
	config.Reference = h2cUnix(s.referenceSock)
	config.Candidate = h2cUnix(s.candidateSock)
	config.Descriptors = s.config.Descriptors
	return config
}

// proxyLogger returns a logger whose output lines are tagged with the proxy name.
func (s *Supervisor) proxyLogger(name string) *slog.Logger {
	return logger.Tagged(logger.New(s.logConfig, newPrefixWriter(s.output, name)))
}

// listenAll opens every address in order, closing any already opened on failure.
func listenAll(ctx context.Context, addresses ...string) ([]net.Listener, error) {
	listeners := make([]net.Listener, 0, len(addresses))
	for _, address := range addresses {
		listener, err := netaddr.ParseListen(address).Listen(ctx)
		if err != nil {
			return nil, errors.Join(errors.Wrapf(err, "listen on %s", address), closeListeners(listeners))
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

// closeListeners closes every listener, joining any errors.
func closeListeners(listeners []net.Listener) error {
	closeErrors := make([]error, 0, len(listeners))
	for _, listener := range listeners {
		closeErrors = append(closeErrors, listener.Close())
	}
	return errors.Join(closeErrors...)
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

func unixListen(path string) string { return "unix:" + path }
func httpUnix(path string) string   { return "http+unix:" + path }
func h2cUnix(path string) string    { return "h2c+unix:" + path }

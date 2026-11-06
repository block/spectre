package proxies

import (
	"context"
	"log/slog"
	"net"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/httpserver"
	"github.com/block/spectre/internal/httpserver/middleware/health"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/reporter"
	"github.com/block/spectre/internal/state"
)

// Wiring is where the proxies listen and where ingress reaches both services.
type Wiring struct {
	// Listen is the public ingress address.
	Listen string
	// Reference and Candidate are the service URLs ingress forwards to.
	Reference string
	Candidate string
	// ReferenceEgress and CandidateEgress receive each service's dependency calls.
	ReferenceEgress string
	CandidateEgress string
	// CandidateRedirect receives the candidate's connections that iptables redirected.
	CandidateRedirect Option[string]
	// EgressHealth serves the egress proxy's own health endpoints.
	EgressHealth string
	// Health serves the health of both proxies.
	Health Option[string]
}

// Mirror runs the ingress and egress proxies and reports their state.
type Mirror struct {
	config    Config
	wiring    Wiring
	log       *slog.Logger
	migration Option[state.State]

	ingress *httpserver.Ingress
	egress  *httpserver.Egress

	cancel  context.CancelFunc
	running int
	exits   chan exit
}

// exit reports a finished proxy.
type exit struct {
	name string
	err  error
}

// New validates config and returns a mirror wired by wiring. The catalog checks
// the migration state, if config names one.
func New(ctx context.Context, config Config, wiring Wiring, catalog *state.Catalog) (*Mirror, error) {
	if err := config.Validate(); err != nil {
		return nil, errors.WithStack(err)
	}
	migration := None[state.State]()
	if config.StateFile != "" {
		loaded, err := state.Load(config.StateFile, catalog)
		if err != nil {
			return nil, errors.Wrapf(err, "load migration state %s", config.StateFile)
		}
		migration = Some(loaded)
	}
	return &Mirror{
		config:    config,
		wiring:    wiring,
		log:       logger.FromContext(ctx).With(logger.SubsystemKey, "mirror"),
		migration: migration,
		cancel:    func() {},
		exits:     make(chan exit, 3),
	}, nil
}

// NewProxy validates config and returns a mirror of services that run elsewhere.
func NewProxy(ctx context.Context, config ProxyConfig, catalog *state.Catalog) (*Mirror, error) {
	if err := config.Validate(); err != nil {
		return nil, errors.WithStack(err)
	}
	return New(ctx, config.Mirror, config.wiring(), catalog)
}

// Run starts the proxies, then serves until the context is cancelled or a proxy exits.
func (m *Mirror) Run(ctx context.Context) error {
	if err := m.Start(ctx); err != nil {
		return err
	}
	return m.Wait(ctx)
}

// Start opens every listener and serves the proxies in the background. Ingress is
// ready once the reference accepts, and mirrors once the candidate does.
func (m *Mirror) Start(ctx context.Context) error {
	ctx, m.cancel = context.WithCancel(ctx)
	if err := m.startAll(ctx); err != nil {
		m.cancel()
		m.drain()
		return err
	}
	m.log.InfoContext(ctx, "Mirror started", "ingress", m.wiring.Listen, "health", m.wiring.Health.Default(""))
	return nil
}

// Wait blocks until the context is cancelled or a proxy exits, then stops and
// waits for the rest.
func (m *Mirror) Wait(ctx context.Context) error {
	defer m.drain()
	defer m.cancel()
	select {
	case <-ctx.Done():
		return nil
	case finished := <-m.exits:
		m.running--
		m.log.WarnContext(ctx, "Proxy exited; shutting down", "name", finished.name, "error", finished.err)
		return errors.Wrapf(finished.err, "%s exited", finished.name)
	}
}

// startAll serves egress first, so services can reach dependencies as they start.
func (m *Mirror) startAll(ctx context.Context) error {
	if err := m.startEgress(ctx); err != nil {
		return err
	}
	if err := m.startIngress(ctx); err != nil {
		return err
	}
	if err := m.startHealth(ctx); err != nil {
		return err
	}
	m.startReporter(ctx)
	return nil
}

// startEgress builds the egress proxy, opens its listeners, and serves it.
func (m *Mirror) startEgress(ctx context.Context) error {
	config := m.egressConfig()
	transport := httpserver.NewTransport(config.TransportConfig)
	proxyContext := logger.WithSubsystem(ctx, "egress")
	handler, err := httpserver.NewEgress(proxyContext, config, m.engineConfig(), transport)
	if err != nil {
		transport.CloseIdleConnections()
		return errors.Wrap(err, "configure egress")
	}
	m.egress = handler
	addresses := []string{config.ReferenceListen, config.CandidateListen, config.HealthListen}
	if redirect, ok := m.wiring.CandidateRedirect.Get(); ok {
		addresses = append(addresses, redirect)
	}
	listeners, err := listenAll(ctx, addresses...)
	if err != nil {
		transport.CloseIdleConnections()
		return errors.Wrap(err, "listen for egress")
	}
	redirect := None[net.Listener]()
	if len(listeners) > 3 {
		redirect = Some(listeners[3])
	}
	m.serve(ctx, "egress", transport.CloseIdleConnections, func() error {
		return handler.Serve(proxyContext, listeners[0], listeners[1], listeners[2], redirect)
	})
	return nil
}

// startIngress builds the ingress proxy, opens its listener, and serves it.
func (m *Mirror) startIngress(ctx context.Context) error {
	config := m.ingressConfig()
	transport := httpserver.NewTransport(config.TransportConfig)
	loader := Some[httpserver.DescriptorLoader](descriptors.NewReflectionLoader())
	proxyContext := logger.WithSubsystem(ctx, "ingress")
	handler, err := httpserver.NewIngress(proxyContext, config, m.engineConfig(), transport, loader, m.migration)
	if err != nil {
		transport.CloseIdleConnections()
		return errors.Wrap(err, "configure ingress")
	}
	m.ingress = handler
	listener, err := netaddr.ParseListen(config.Listen).Listen(ctx)
	if err != nil {
		transport.CloseIdleConnections()
		return errors.Wrap(err, "listen for ingress")
	}
	m.serve(ctx, "ingress", transport.CloseIdleConnections, func() error {
		return handler.Serve(proxyContext, listener)
	})
	return nil
}

// startHealth serves the health of both proxies, when the wiring asks for it.
func (m *Mirror) startHealth(ctx context.Context) error {
	address, ok := m.wiring.Health.Get()
	if !ok {
		return nil
	}
	listener, err := netaddr.ParseListen(address).Listen(ctx)
	if err != nil {
		return errors.Wrap(err, "listen for health")
	}
	handler := health.NewAll(m.ingress.Health(), m.egress.Health())
	config := httpserver.NewConfig()
	healthContext := logger.WithSubsystem(ctx, "health")
	lifecycle := httpserver.NewLifecycle(func(context.Context) {}, func() {}, func(context.Context) error { return nil })
	m.serve(ctx, "health", func() {}, func() error {
		return errors.Wrap(httpserver.Serve(healthContext, config, []httpserver.Binding{httpserver.NewBinding(listener, handler)}, lifecycle), "serve health")
	})
	return nil
}

// startReporter streams the mirror's state, with ingress call counts and both
// proxies' quarantines, to the SPECTRE server. Reporting never stops the mirror.
func (m *Mirror) startReporter(ctx context.Context) {
	reporter := reporter.New(m.config.Server, client.New(m.config.Server.Client), m.ingress.Calls,
		reporter.Source{Direction: model.Ingress, Quarantined: m.ingress.Candidates().Quarantined()},
		reporter.Source{Direction: model.Egress, Quarantined: m.egress.Candidates().Quarantined()},
	)
	go reporter.Run(logger.WithLogger(ctx, m.log))
	m.log.InfoContext(ctx, "Reporting to the SPECTRE server", "url", m.config.Server.Client.URL)
}

// serve runs a server in the background, reporting its exit and calling release
// once it stops.
func (m *Mirror) serve(ctx context.Context, name string, release func(), serve func() error) {
	m.running++
	go func() {
		defer release()
		m.exits <- exit{name: name, err: serve()}
	}()
	m.log.InfoContext(ctx, "Started proxy", "name", name)
}

// drain waits for the remaining proxies to exit after the context is cancelled.
func (m *Mirror) drain() {
	for m.running > 0 {
		<-m.exits
		m.running--
	}
}

// engineConfig builds the comparison engine configuration shared by both proxies.
func (m *Mirror) engineConfig() protocol.Config {
	config := protocol.NewConfig()
	config.Comparison.ScriptsDir = m.config.ScriptsDir
	config.Comparison.Schema.SchemaDir = m.config.SchemaDir
	config.MatchWindow = m.config.MatchWindow
	return config
}

// egressConfig builds the egress proxy configuration from the wiring.
func (m *Mirror) egressConfig() httpserver.EgressConfig {
	config := httpserver.NewEgressConfig()
	config.ReferenceListen = m.wiring.ReferenceEgress
	config.CandidateListen = m.wiring.CandidateEgress
	config.HealthListen = m.wiring.EgressHealth
	config.CandidateRedirectListen = m.wiring.CandidateRedirect.Default("")
	config.Destinations = m.config.Destinations
	config.Descriptors = m.config.Descriptors
	return config
}

// ingressConfig builds the ingress proxy configuration from the wiring.
func (m *Mirror) ingressConfig() httpserver.IngressConfig {
	config := httpserver.NewIngressConfig()
	config.Listen = m.wiring.Listen
	config.Reference = m.wiring.Reference
	config.Candidate = m.wiring.Candidate
	config.Reflection = m.config.Reflection
	config.CandidateStartupTimeout = m.config.CandidateStartupTimeout
	config.Descriptors = m.config.Descriptors
	return config
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

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
	"github.com/block/spectre/internal/mysqlserver"
	"github.com/block/spectre/internal/mysqlserver/exact"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/reporter"
	"github.com/block/spectre/internal/state"
)

// Mirror runs the ingress and egress proxies and reports their state.
type Mirror struct {
	config    ProxyConfig
	log       *slog.Logger
	migration Option[state.State]

	ingress *httpserver.Ingress
	egress  *httpserver.Egress

	cancel  context.CancelFunc
	running int
	exits   chan exit
}

// Listeners are the open listeners of a mirror's proxies. The MySQL listeners are
// present only when MySQL is proxied.
type Listeners struct {
	Ingress         net.Listener
	ReferenceEgress net.Listener
	CandidateEgress net.Listener
	EgressHealth    net.Listener
	Health          net.Listener
	MySQLReference  Option[net.Listener]
	MySQLCandidate  Option[net.Listener]
}

// close closes every listener that a stopped proxy has not, joining any errors.
func (l Listeners) close() error {
	closeErrors := []error{}
	listeners := []net.Listener{l.Ingress, l.ReferenceEgress, l.CandidateEgress, l.EgressHealth, l.Health}
	for _, optional := range []Option[net.Listener]{l.MySQLReference, l.MySQLCandidate} {
		if listener, ok := optional.Get(); ok {
			listeners = append(listeners, listener)
		}
	}
	for _, listener := range listeners {
		if err := listener.Close(); !errors.Is(err, net.ErrClosed) {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

// exit reports a finished proxy.
type exit struct {
	name string
	err  error
}

// New validates config and returns a mirror of services that run elsewhere. The
// catalog checks the migration state, if config names one.
func New(ctx context.Context, config ProxyConfig, catalog *state.Catalog) (*Mirror, error) {
	if err := config.Validate(); err != nil {
		return nil, errors.WithStack(err)
	}
	migration := None[state.State]()
	if stateFile := config.Mirror.StateFile; stateFile != "" {
		loaded, err := state.Load(stateFile, catalog)
		if err != nil {
			return nil, errors.Wrapf(err, "load migration state %s", stateFile)
		}
		migration = Some(loaded)
	}
	return &Mirror{
		config:    config,
		log:       logger.FromContext(ctx).With(logger.SubsystemKey, "mirror"),
		migration: migration,
		cancel:    func() {},
		exits:     make(chan exit, 4),
	}, nil
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
	listeners, err := m.listen(ctx)
	if err != nil {
		return err
	}
	return m.StartListeners(ctx, listeners)
}

// StartListeners serves the proxies on listeners in the background, ignoring
// the configured addresses. The proxies close the listeners when they stop.
func (m *Mirror) StartListeners(ctx context.Context, listeners Listeners) error {
	ctx, m.cancel = context.WithCancel(ctx)
	if err := m.startAll(ctx, listeners); err != nil {
		m.cancel()
		m.drain()
		return errors.Join(err, listeners.close())
	}
	m.log.InfoContext(ctx, "Mirror started", "ingress", m.config.Listen, "health", m.config.HealthListen)
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

// listen opens the configured listener of every proxy.
func (m *Mirror) listen(ctx context.Context) (Listeners, error) {
	opened, err := listenAll(ctx, m.config.Listen, m.config.ReferenceEgressListen, m.config.CandidateEgressListen, m.config.EgressHealthListen, m.config.HealthListen)
	if err != nil {
		return Listeners{}, err
	}
	listeners := Listeners{Ingress: opened[0], ReferenceEgress: opened[1], CandidateEgress: opened[2], EgressHealth: opened[3], Health: opened[4]}
	if mysql := m.config.Mirror.MySQL; mysql.Enabled() {
		databases, err := listenAll(ctx, mysql.ReferenceListen, mysql.CandidateListen)
		if err != nil {
			return Listeners{}, errors.Join(err, listeners.close())
		}
		listeners.MySQLReference, listeners.MySQLCandidate = Some(databases[0]), Some(databases[1])
	}
	return listeners, nil
}

// startAll serves egress first, so services can reach dependencies as they start.
func (m *Mirror) startAll(ctx context.Context, listeners Listeners) error {
	if err := m.startEgress(ctx, listeners); err != nil {
		return err
	}
	if err := m.startMySQL(ctx, listeners.MySQLReference, listeners.MySQLCandidate); err != nil {
		return err
	}
	if err := m.startIngress(ctx, listeners.Ingress); err != nil {
		return err
	}
	m.startHealth(ctx, listeners.Health)
	m.startReporter(ctx)
	return nil
}

// startEgress builds the egress proxy and serves it.
func (m *Mirror) startEgress(ctx context.Context, listeners Listeners) error {
	config := m.egressConfig()
	transport := httpserver.NewTransport(config.TransportConfig)
	proxyContext := logger.WithSubsystem(ctx, "egress")
	handler, err := httpserver.NewEgress(proxyContext, config, m.engineConfig(), transport)
	if err != nil {
		transport.CloseIdleConnections()
		return errors.Wrap(err, "configure egress")
	}
	m.egress = handler
	m.serve(ctx, "egress", transport.CloseIdleConnections, func() error {
		return handler.Serve(proxyContext, listeners.ReferenceEgress, listeners.CandidateEgress, listeners.EgressHealth)
	})
	return nil
}

// startMySQL serves the MySQL proxy, if MySQL is proxied. It pairs statements in the egress
// proxy's engine, and its quarantines are the egress proxy's.
func (m *Mirror) startMySQL(ctx context.Context, referenceListener, candidateListener Option[net.Listener]) error {
	config := m.config.Mirror.MySQL
	if !config.Enabled() {
		return nil
	}
	reference, hasReference := referenceListener.Get()
	candidate, hasCandidate := candidateListener.Get()
	if !hasReference || !hasCandidate {
		return errors.New("MySQL is proxied, but its listeners are not open")
	}
	server, err := mysqlserver.New(config, exact.New(), m.egress.Engine(), m.egress.Candidates())
	if err != nil {
		return errors.Wrap(err, "configure MySQL")
	}
	proxyContext := logger.WithSubsystem(ctx, "mysql")
	m.serve(ctx, "mysql", func() {}, func() error {
		return errors.Wrap(server.Serve(proxyContext, reference, candidate), "serve MySQL")
	})
	return nil
}

// startIngress builds the ingress proxy and serves it on listener.
func (m *Mirror) startIngress(ctx context.Context, listener net.Listener) error {
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
	m.serve(ctx, "ingress", transport.CloseIdleConnections, func() error {
		return handler.Serve(proxyContext, listener)
	})
	return nil
}

// startHealth serves the health of both proxies on listener, for probes.
func (m *Mirror) startHealth(ctx context.Context, listener net.Listener) {
	handler := health.NewAll(m.ingress.Health(), m.egress.Health())
	config := httpserver.NewConfig()
	healthContext := logger.WithSubsystem(ctx, "health")
	lifecycle := httpserver.NewLifecycle(func(context.Context) {}, func() {}, func(context.Context) error { return nil })
	m.serve(ctx, "health", func() {}, func() error {
		return errors.Wrap(httpserver.Serve(healthContext, config, []httpserver.Binding{httpserver.NewBinding(listener, handler)}, lifecycle), "serve health")
	})
}

// startReporter streams the mirror's state, with ingress call counts and both
// proxies' quarantines, to the SPECTRE server. Reporting never stops the mirror.
func (m *Mirror) startReporter(ctx context.Context) {
	reporter := reporter.New(m.config.Mirror.Server, client.New(m.config.Mirror.Server.Client), m.ingress.Calls,
		reporter.Source{Direction: model.Ingress, Quarantined: m.ingress.Candidates().Quarantined()},
		reporter.Source{Direction: model.Egress, Quarantined: m.egress.Candidates().Quarantined()},
	)
	go reporter.Run(logger.WithLogger(ctx, m.log))
	m.log.InfoContext(ctx, "Reporting to the SPECTRE server", "url", m.config.Mirror.Server.Client.URL)
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
	config.Comparison.ScriptsDir = m.config.Mirror.ScriptsDir
	config.Comparison.Schema.SchemaDir = m.config.Mirror.SchemaDir
	config.Comparison.Variables = m.config.Mirror.ScriptVariables
	config.MatchWindow = m.config.Mirror.MatchWindow
	return config
}

// egressConfig builds the egress proxy configuration.
func (m *Mirror) egressConfig() httpserver.EgressConfig {
	config := httpserver.NewEgressConfig()
	config.ReferenceListen = m.config.ReferenceEgressListen
	config.CandidateListen = m.config.CandidateEgressListen
	config.HealthListen = m.config.EgressHealthListen
	config.Destinations = m.config.Mirror.Destinations
	config.Descriptors = m.config.Mirror.Descriptors
	return config
}

// ingressConfig builds the ingress proxy configuration.
func (m *Mirror) ingressConfig() httpserver.IngressConfig {
	config := httpserver.NewIngressConfig()
	config.Listen = m.config.Listen
	config.Reference = m.config.ReferenceURL
	config.Candidate = m.config.CandidateURL
	config.Reflection = m.config.Mirror.Reflection
	config.CandidateStartupTimeout = m.config.Mirror.CandidateStartupTimeout
	config.Descriptors = m.config.Mirror.Descriptors
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

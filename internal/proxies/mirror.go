package proxies

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/httpservice"
	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/schema"
	"github.com/block/spectre/internal/server/client"
	"github.com/block/spectre/internal/server/reporter"
	"github.com/block/spectre/internal/state"
	"github.com/block/spectre/internal/statsd"
)

// shutdownTimeout bounds how long stop waits for work that outlives the servers.
const shutdownTimeout = 10 * time.Second

// Mirror runs every registered protocol's proxies and reports their state.
type Mirror struct {
	config    Config
	registry  *protocols.Registry
	log       *slog.Logger
	migration Option[state.State]

	// shared is set once the mirror starts, and is closed and shut down by stop.
	shared Option[*shared]
	// ready holds the readiness of every side, so stop can mark them unready first.
	ready []*health.Handler

	cancel  context.CancelFunc
	running int
	exits   chan exit
}

// Listeners are the open listeners of a mirror: each provider's, by the names
// its Listens gives them, the two health endpoints', and the StatsD sockets when relayed.
type Listeners struct {
	Providers       map[model.Protocol]map[string]net.Listener
	EgressHealth    net.Listener
	Health          net.Listener
	StatsdReference Option[net.PacketConn]
	StatsdCandidate Option[net.PacketConn]
}

// close closes every listener that a stopped proxy has not, joining any errors.
func (l Listeners) close() error {
	closers := []io.Closer{}
	for _, listener := range []net.Listener{l.EgressHealth, l.Health} {
		if listener != nil {
			closers = append(closers, listener)
		}
	}
	for _, provided := range l.Providers {
		for _, listener := range provided {
			if listener != nil {
				closers = append(closers, listener)
			}
		}
	}
	for _, conn := range []Option[net.PacketConn]{l.StatsdReference, l.StatsdCandidate} {
		if conn, ok := conn.Get(); ok {
			closers = append(closers, conn)
		}
	}
	closeErrors := []error{}
	for _, closer := range closers {
		if err := closer.Close(); !errors.Is(err, net.ErrClosed) {
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

// New validates config and the registry's providers, and returns a mirror of services
// that run elsewhere, proxying each registered protocol.
func New(ctx context.Context, config Config, registry *protocols.Registry) (*Mirror, error) {
	if err := config.Validate(); err != nil {
		return nil, errors.WithStack(err)
	}
	if err := registry.Validate(); err != nil {
		return nil, errors.WithStack(err)
	}
	migration := None[state.State]()
	if stateFile := config.StateFile; stateFile != "" {
		loaded, err := registry.Kinds().Load(stateFile)
		if err != nil {
			return nil, errors.Wrapf(err, "load migration state %s", stateFile)
		}
		migration = Some(loaded)
	}
	return &Mirror{
		config:    config,
		registry:  registry,
		log:       logger.FromContext(ctx).With(logger.SubsystemKey, "mirror"),
		migration: migration,
		cancel:    func() {},
		// Each provider's server exits once, and so do the two health servers and two StatsD relays.
		exits: make(chan exit, len(registry.Protocols())+4),
	}, nil
}

// Run starts the proxies, then serves until the context is cancelled or a proxy exits.
func (m *Mirror) Run(ctx context.Context) error {
	if err := m.Start(ctx); err != nil {
		return err
	}
	return m.Wait(ctx)
}

// Start opens every listener and serves the proxies in the background. Each
// ingress is ready once its reference accepts, and mirrors once its candidate does.
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
		m.stop(ctx)
		return errors.Join(err, listeners.close())
	}
	m.log.InfoContext(ctx, "Mirror started", "health", m.config.HealthListen)
	return nil
}

// Wait blocks until the context is cancelled or a proxy exits, then stops and
// waits for the rest.
func (m *Mirror) Wait(ctx context.Context) error {
	defer m.stop(ctx)
	select {
	case <-ctx.Done():
		return nil
	case finished := <-m.exits:
		m.running--
		m.log.WarnContext(ctx, "Proxy exited; shutting down", "name", finished.name, "error", finished.err)
		return errors.Wrapf(finished.err, "%s exited", finished.name)
	}
}

// listen opens the listeners of every provider and the health endpoints, before
// anything starts, so a bad address fails startup. It closes those already open on failure.
func (m *Mirror) listen(ctx context.Context) (Listeners, error) {
	listeners := Listeners{Providers: map[model.Protocol]map[string]net.Listener{}}
	for _, protocol := range m.registry.Protocols() {
		opened := map[string]net.Listener{}
		listeners.Providers[protocol] = opened
		for _, listen := range m.registry.Listens(protocol) {
			if _, duplicate := opened[listen.Name]; duplicate {
				return Listeners{}, errors.Join(errors.Errorf("%s declares listener %s more than once", protocol, listen.Name), listeners.close())
			}
			listener, err := listenOn(ctx, listen.Address)
			if err != nil {
				return Listeners{}, errors.Join(err, listeners.close())
			}
			opened[listen.Name] = listener
		}
	}
	var err error
	if listeners.EgressHealth, err = listenOn(ctx, m.config.EgressHealthListen); err != nil {
		return Listeners{}, errors.Join(err, listeners.close())
	}
	if listeners.Health, err = listenOn(ctx, m.config.HealthListen); err != nil {
		return Listeners{}, errors.Join(err, listeners.close())
	}
	if listeners.StatsdReference, err = listenPacketOn(ctx, m.config.Statsd.ReferenceListen); err != nil {
		return Listeners{}, errors.Join(err, listeners.close())
	}
	if listeners.StatsdCandidate, err = listenPacketOn(ctx, m.config.Statsd.CandidateListen); err != nil {
		return Listeners{}, errors.Join(err, listeners.close())
	}
	return listeners, nil
}

// startAll builds every provider's proxies on the shared machinery, then serves
// them all and the health endpoints.
func (m *Mirror) startAll(ctx context.Context, listeners Listeners) error {
	if err := m.checkListeners(listeners); err != nil {
		return err
	}
	built, err := newShared(logger.WithSubsystem(ctx, "engine"), m.engineConfig(), m.config.Candidates, m.registry.Bindings(), m.migration)
	if err != nil {
		return err
	}
	m.shared = Some(built)
	protocolsToServe := m.registry.Protocols()
	servers := make([]protocols.Server, 0, len(protocolsToServe))
	var egress []*health.Handler
	for _, protocol := range protocolsToServe {
		runtime := m.runtime(built, protocol)
		for _, side := range []Option[protocols.Side]{runtime.Ingress, runtime.Egress} {
			if side, ok := side.Get(); ok {
				m.ready = append(m.ready, side.Health)
			}
		}
		if side, ok := runtime.Egress.Get(); ok {
			egress = append(egress, side.Health)
		}
		server, err := m.registry.New(ctx, protocol, runtime)
		if err != nil {
			return errors.WithStack(err)
		}
		servers = append(servers, server)
	}
	for i, protocol := range protocolsToServe {
		m.serve(ctx, string(protocol), func() error { return servers[i].Serve(ctx, listeners.Providers[protocol]) })
	}
	m.serveHealth(ctx, "health", listeners.Health, m.ready)
	m.serveHealth(ctx, "egress-health", listeners.EgressHealth, egress)
	m.serveStatsd(ctx, listeners.StatsdReference, listeners.StatsdCandidate)
	m.startReporter(ctx, built)
	return nil
}

// checkListeners checks that every listener a provider names is open, and that
// exactly the configured StatsD sockets are.
func (m *Mirror) checkListeners(listeners Listeners) error {
	for _, protocol := range m.registry.Protocols() {
		for _, listen := range m.registry.Listens(protocol) {
			if listeners.Providers[protocol][listen.Name] == nil {
				return errors.Errorf("%s listener %s is not open", protocol, listen.Name)
			}
		}
	}
	if listeners.StatsdReference.Ok() != (m.config.Statsd.ReferenceListen != "") {
		return errors.New("StatsD reference socket must be open exactly when it is configured")
	}
	if listeners.StatsdCandidate.Ok() != (m.config.Statsd.CandidateListen != "") {
		return errors.New("StatsD candidate socket must be open exactly when it is configured")
	}
	return nil
}

// serveStatsd forwards the reference's StatsD metrics to the agent and discards the candidate's.
func (m *Mirror) serveStatsd(ctx context.Context, reference Option[net.PacketConn], candidate Option[net.PacketConn]) {
	statsdContext := logger.WithSubsystem(ctx, "statsd")
	if conn, ok := reference.Get(); ok {
		forward := netaddr.ParsePacket(m.config.Statsd.Forward)
		m.serve(ctx, "statsd-reference", func() error { return statsd.Forward(statsdContext, conn, forward) })
	}
	if conn, ok := candidate.Get(); ok {
		m.serve(ctx, "statsd-candidate", func() error { return statsd.Discard(statsdContext, conn) })
	}
}

// runtime gives a protocol's provider the sides that its kinds name, and nothing of any other protocol.
func (m *Mirror) runtime(built *shared, protocol model.Protocol) protocols.Runtime {
	runtime := protocols.Runtime{Schema: built.Engine.Schema(), Scripts: built.Engine.Scripts()}
	kinds := m.registry.Kinds()
	if _, ok := kinds.Kind(model.Ingress, protocol); ok {
		runtime.Ingress = Some(m.ingressSide(built, protocol))
	}
	if _, ok := kinds.Kind(model.Egress, protocol); ok {
		runtime.Egress = Some(m.egressSide(built.Engine, built.EgressCandidates, protocol))
	}
	return runtime
}

// ingressSide is the ingress machinery of protocol. Its candidates are quarantined through
// the node quarantiner, which names the migration state node that owns each route.
func (m *Mirror) ingressSide(built *shared, protocol model.Protocol) protocols.Side {
	return protocols.Side{
		Reference:      built.Engine.IngressReference(protocol),
		Candidate:      built.Engine.IngressCandidate(protocol),
		Candidates:     built.IngressCandidates,
		Quarantiner:    built.IngressQuarantiner,
		Health:         health.New(http.NotFoundHandler()),
		Nodes:          m.nodes(model.Ingress, protocol),
		SourceResponse: func(model.Route) Option[schema.TypeRef] { return None[schema.TypeRef]() },
	}
}

// egressSide is the egress machinery of protocol.
func (m *Mirror) egressSide(comparison *engine.Engine, candidates *proxy.Candidates, protocol model.Protocol) protocols.Side {
	return protocols.Side{
		Reference:   comparison.EgressReference(protocol),
		Candidate:   comparison.EgressCandidate(protocol),
		Candidates:  candidates,
		Quarantiner: candidates,
		Health:      health.New(http.NotFoundHandler()),
		Nodes:       m.nodes(model.Egress, protocol),
		SourceResponse: func(route model.Route) Option[schema.TypeRef] {
			return comparison.EgressSourceResponse(protocol, route)
		},
	}
}

// nodes are the migration state's nodes with protocol's kind in direction, when the mirror has a state.
func (m *Mirror) nodes(direction model.Direction, protocol model.Protocol) Option[[]state.KindNode] {
	migration, ok := m.migration.Get()
	if !ok {
		return None[[]state.KindNode]()
	}
	return Some(migration.Nodes(state.Kind{Direction: direction, Protocol: protocol}))
}

// serveHealth serves the combined health of handlers on listener, for probes.
func (m *Mirror) serveHealth(ctx context.Context, name string, listener net.Listener, handlers []*health.Handler) {
	handler := health.NewAll(handlers...)
	healthContext := logger.WithSubsystem(ctx, name)
	lifecycle := httpservice.NewLifecycle(func(context.Context) {}, func() {}, func(context.Context) error { return nil })
	m.serve(ctx, name, func() error {
		return errors.Wrapf(httpservice.Serve(healthContext, httpservice.NewServeConfig(), []httpservice.Binding{httpservice.NewBinding(listener, handler)}, lifecycle), "serve %s", name)
	})
}

// startReporter streams the mirror's state, with ingress call counts and both
// directions' quarantines, to the SPECTRE server. Reporting never stops the mirror.
func (m *Mirror) startReporter(ctx context.Context, shared *shared) {
	reporter := reporter.New(m.config.Server, client.New(m.config.Server.Client), shared.Calls.Calls,
		reporter.Source{Direction: model.Ingress, Quarantined: shared.IngressCandidates.Quarantined()},
		reporter.Source{Direction: model.Egress, Quarantined: shared.EgressCandidates.Quarantined()},
	)
	go reporter.Run(logger.WithLogger(ctx, m.log))
	m.log.InfoContext(ctx, "Reporting to the SPECTRE server", "url", m.config.Server.Client.URL)
}

// serve runs a server in the background and reports its exit.
func (m *Mirror) serve(ctx context.Context, name string, serve func() error) {
	m.running++
	go func() {
		m.exits <- exit{name: name, err: serve()}
	}()
	m.log.InfoContext(ctx, "Started proxy", "name", name)
}

// stop marks proxies unready, closes the engine and candidates, then cancels the
// proxies and waits for them, then waits for what outlives them. The order avoids quarantines.
func (m *Mirror) stop(ctx context.Context) {
	for _, handler := range m.ready {
		handler.SetReady(false)
	}
	built, started := m.shared.Get()
	if started {
		built.close()
	}
	m.cancel()
	for m.running > 0 {
		<-m.exits
		m.running--
	}
	if !started {
		return
	}
	shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := built.shutdown(shutdownContext); err != nil {
		m.log.WarnContext(ctx, "Candidate work did not shut down", "error", err)
	}
}

// engineConfig builds the comparison engine configuration shared by every protocol.
func (m *Mirror) engineConfig() engine.Config {
	config := engine.NewConfig()
	config.Comparison.ScriptsDir = m.config.ScriptsDir
	config.Comparison.Schema.SchemaDir = m.config.SchemaDir
	config.Comparison.Variables = m.config.ScriptVariables
	config.MatchWindow = m.config.MatchWindow
	return config
}

// listenPacketOn opens the datagram socket at address, or none when address is empty.
func listenPacketOn(ctx context.Context, address string) (Option[net.PacketConn], error) {
	if address == "" {
		return None[net.PacketConn](), nil
	}
	conn, err := netaddr.ParsePacket(address).Listen(ctx)
	if err != nil {
		return None[net.PacketConn](), errors.Wrapf(err, "listen on %s", address)
	}
	return Some(conn), nil
}

// listenOn opens the listener at address.
func listenOn(ctx context.Context, address string) (net.Listener, error) {
	listener, err := netaddr.ParseListen(address).Listen(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "listen on %s", address)
	}
	return listener, nil
}

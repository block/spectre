package mysql

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/client"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"
	"github.com/pingcap/tidb/pkg/parser/charset"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/httpservice/health"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols"
	"github.com/block/spectre/internal/protocols/mysql/exact"
	"github.com/block/spectre/internal/protocols/mysql/normaliser"
	"github.com/block/spectre/internal/protocols/mysql/scripted"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/schema"
)

// dialTimeout limits connecting to the database when a reference connection authenticates.
const dialTimeout = 10 * time.Second

// fallbackVersion is the MySQL version the proxy reports while it cannot reach the database.
const fallbackVersion = "8.0.0"

// Server serves reference and candidate MySQL connections on separate listeners.
// Candidate statements never reach a database under any condition.
type Server struct {
	// upstream is the database that reference connections reach.
	upstream string
	// user and passwordFile are the proxy's credentials for the database.
	user         string
	passwordFile string
	upstreamTLS  Option[*tls.Config]
	candidates   *proxy.Candidates
	// health is the egress readiness, which Serve sets once the listeners are served.
	health     *health.Handler
	threads    *threads
	reference  engine.Acceptor
	candidate  engine.Acceptor
	budget     *proxy.Budget
	normaliser normaliser.Normaliser
	parser     *normaliser.Parser
	// schema types the pairings egress hashes.
	schema *schema.Schema
	// scope pairs every unit with every other, because statements carry no request ID.
	scope engine.RequestID
	// version is the database's version once the proxy has connected to it.
	versionMu sync.Mutex
	version   Option[string]
}

// New returns a MySQL proxy that pairs the units of statements on the egress side, as the replacements
// that scripts declared through the handle say. The mirror owns the side's candidates and engine.
func New(config Config, handle scripted.Handle, runtime protocols.Runtime, side protocols.Side) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !config.Enabled() {
		return nil, errors.New("MySQL upstream is required")
	}
	upstreamTLS, err := upstreamTLS(config.UpstreamCA)
	if err != nil {
		return nil, err
	}
	if runtime.Scripts == nil || runtime.Schema == nil || side.Candidates == nil || side.Health == nil {
		return nil, errors.New("scripts, schema, candidates and health are required")
	}
	statements, err := scripted.New(runtime.Scripts, handle, exact.New())
	if err != nil {
		return nil, errors.Wrap(err, "configure MySQL statement replacements")
	}
	scope, err := typeid.New[engine.RequestID]()
	if err != nil {
		return nil, errors.Wrap(err, "name the pairing scope")
	}
	return &Server{
		upstream:     config.Upstream,
		user:         config.UpstreamUser,
		passwordFile: config.UpstreamPasswordFile,
		upstreamTLS:  upstreamTLS,
		candidates:   side.Candidates,
		health:       side.Health,
		threads:      newThreads(),
		reference:    side.Reference,
		candidate:    side.Candidate,
		budget:       proxy.NewBudget(config.SavedRepliesBytes),
		normaliser:   statements,
		parser:       normaliser.NewParser(),
		schema:       runtime.Schema,
		scope:        scope,
	}, nil
}

// Serve accepts reference and candidate connections until the context is cancelled,
// then closes them and waits for their handlers. Egress is ready once it serves.
func (s *Server) Serve(ctx context.Context, listeners map[string]net.Listener) error {
	reference, candidate := listeners[ReferenceListener], listeners[CandidateListener]
	if reference == nil || candidate == nil {
		return errors.New("MySQL listeners are not open")
	}
	stop := context.AfterFunc(ctx, func() {
		_ = reference.Close()
		_ = candidate.Close()
	})
	defer stop()
	connections := sync.WaitGroup{}
	defer connections.Wait()
	failures := make(chan error, 2)
	go func() { failures <- s.accept(ctx, reference, &connections, s.serveReference) }()
	go func() { failures <- s.accept(ctx, candidate, &connections, s.serveCandidate) }()
	s.health.SetReady(true)
	first := <-failures
	// One listener failing stops the other, so both accept loops always finish.
	_ = reference.Close()
	_ = candidate.Close()
	return errors.Join(first, <-failures)
}

// accept serves each connection of listener in the background until it closes.
func (s *Server) accept(ctx context.Context, listener net.Listener, connections *sync.WaitGroup,
	serve func(ctx context.Context, conn net.Conn),
) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil //nolint:nilerr // A closed listener is how serving ends.
			}
			return errors.Wrapf(err, "accept on %s", listener.Addr())
		}
		connections.Go(func() {
			// Closing the connection unblocks a handler that is reading from it.
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			defer conn.Close()
			// A malformed packet ends its own connection, never the proxy.
			defer func() {
				if failure := recover(); failure != nil {
					logger.FromContext(ctx).ErrorContext(ctx, "MySQL connection failed", "panic", failure)
				}
			}()
			serve(ctx, conn)
		})
	}
}

// serveReference accepts a reference connection with any credentials, connects to
// the database with the proxy's own, and then relays between them.
func (s *Server) serveReference(ctx context.Context, conn net.Conn) {
	from := bufio.NewReader(conn)
	thread := s.threads.reserve()
	asked, err := greet(conn, from, s.serverVersion(ctx), thread)
	if err != nil {
		if ctx.Err() == nil {
			logger.FromContext(ctx).WarnContext(ctx, "MySQL reference handshake failed", "error", err)
		}
		return
	}
	upstream, err := s.connect(ctx, asked)
	if err != nil {
		logger.FromContext(ctx).WarnContext(ctx, "Reference connection has no database", "error", err)
		if err := writePacket(conn, asked.sequence, errorPayload(refusedConnection(err))); err != nil {
			logger.FromContext(ctx).DebugContext(ctx, "Reference left before its refusal", "error", err)
		}
		return
	}
	defer upstream.Close()
	if err := writePacket(conn, asked.sequence, okPayload(asked.capability)); err != nil {
		return
	}
	release := s.threads.connect(thread, upstream.GetConnectionID())
	defer release()
	session := s.normaliser.NewSession()
	if asked.database != "" {
		// The database accepted the login's database, so it is the default.
		if _, err := session.Reference(ctx, normaliser.UseDatabase{Name: asked.database}, normaliser.OK{}); err != nil {
			logger.FromContext(ctx).WarnContext(ctx, "Reference database is not followed", "error", err)
		}
	}
	relay(ctx, s.threads, newObserver(ctx, s, session, asked.capability), conn, from, upstream.Conn.Conn)
}

// refusedConnection is the error a reference gets when the proxy cannot connect it:
// the database's own, or one that says the proxy failed.
func refusedConnection(err error) error {
	if reported, ok := errors.AsType[*mysql.MyError](err); ok {
		return reported
	}
	return mysql.NewError(mysql.ER_UNKNOWN_ERROR, "SPECTRE: the proxy cannot connect to the database")
}

// connect connects to the database with the reference's collation, database and
// features, and checks that the database uses the same features.
func (s *Server) connect(ctx context.Context, login login) (*client.Conn, error) {
	collation, err := charset.GetCollationByID(int(login.collation))
	if err != nil {
		return nil, errors.Errorf("the proxy does not know MySQL collation %d", login.collation)
	}
	upstream, err := s.dial(ctx, login.database, func(c *client.Conn) error {
		for feature := uint32(1); feature != 0; feature <<= 1 {
			switch {
			case passedCapabilities&feature == 0:
			case login.capability&feature == 0:
				c.UnsetCapability(feature)
			case feature != mysql.CLIENT_DEPRECATE_EOF:
				// The client asks for end-of-file deprecation unless it is unset.
				if err := c.SetCapability(feature); err != nil {
					return errors.Wrapf(err, "ask for MySQL capability %s", mysql.CapNames[feature])
				}
			}
		}
		c.UnsetCapability(heldBackCapabilities)
		c.SetAttributes(login.attributes)
		return errors.WithStack(c.SetCollation(collation.Name))
	})
	if err != nil {
		return nil, err
	}
	used := negotiated(upstream) & (passedCapabilities | heldBackCapabilities)
	if used != login.capability&passedCapabilities {
		_ = upstream.Close()
		return nil, errors.Errorf("the database uses MySQL capabilities %#x where the reference asked for %#x",
			used, login.capability&passedCapabilities)
	}
	return upstream, nil
}

// negotiated returns the capabilities that a database connection uses.
func negotiated(upstream *client.Conn) uint32 {
	used := uint32(0)
	names := strings.Split(upstream.CapabilityString(), "|")
	for capability, name := range mysql.CapNames {
		if slices.Contains(names, name) {
			used |= capability
		}
	}
	return used
}

// dial connects to the database with the proxy's credentials, and selects database
// if one is named. The password file is read for every connection, so it can be rotated.
func (s *Server) dial(ctx context.Context, database string, options ...client.Option) (*client.Conn, error) {
	password, err := os.ReadFile(s.passwordFile)
	if err != nil {
		return nil, errors.Wrap(err, "read the MySQL password file")
	}
	if config, ok := s.upstreamTLS.Get(); ok {
		options = append(options, func(c *client.Conn) error {
			c.SetTLSConfig(config)
			return nil
		})
	}
	upstream, err := client.ConnectWithContext(ctx, s.upstream, s.user, strings.TrimRight(string(password), "\r\n"), database,
		dialTimeout, options...)
	return upstream, errors.WithStack(err)
}

// serverVersion returns the database's version, which clients adapt their statements
// to. It connects once to read it, and reports a fallback while it cannot.
func (s *Server) serverVersion(ctx context.Context) string {
	s.versionMu.Lock()
	defer s.versionMu.Unlock()
	if version, ok := s.version.Get(); ok {
		return version
	}
	upstream, err := s.dial(ctx, "")
	if err != nil {
		return fallbackVersion
	}
	defer upstream.Close()
	s.version = Some(upstream.GetServerVersion())
	return upstream.GetServerVersion()
}

// serveCandidate answers a candidate connection without a database. It accepts any
// credentials, and reports the version the reference sees.
func (s *Server) serveCandidate(ctx context.Context, conn net.Conn) {
	wire := server.NewServerWithAuth(s.serverVersion(ctx), mysql.DEFAULT_COLLATION_ID, mysql.AUTH_NATIVE_PASSWORD, nil, nil, acceptAny{})
	s.serve(ctx, conn, wire, anyUser{}, newCandidateConn(ctx, s, s.normaliser.NewSession()))
}

// serve runs the wire protocol on a connection until it closes.
func (s *Server) serve(ctx context.Context, conn net.Conn, wireServer *server.Server, users server.AuthenticationHandler,
	handler server.Handler,
) {
	wire, err := wireServer.NewCustomizedConn(conn, users, handler)
	if err != nil {
		if ctx.Err() == nil {
			logger.FromContext(ctx).WarnContext(ctx, "MySQL handshake failed", "error", err)
		}
		return
	}
	for ctx.Err() == nil && !wire.Closed() {
		if err := wire.HandleCommand(); err != nil {
			return
		}
	}
}

// offering reports whether the candidate still takes units, so reference replies
// are worth reading.
func (s *Server) offering() bool {
	return !s.candidates.QuarantineStatus().Ok()
}

// command is what a normaliser sees of a text statement or database selection.
func (s *Server) command(code byte, text string) normaliser.Command { //nolint:ireturn // Commands are a sum type.
	if code == mysql.COM_INIT_DB {
		return normaliser.UseDatabase{Name: text}
	}
	return s.parser.Parse(text)
}

// reserve claims size bytes of the saved replies budget, and reports whether they fit.
func (s *Server) reserve(size int) (reserved bool) {
	return s.budget.Reserve(size)
}

// release returns size bytes that reserve claimed.
func (s *Server) release(size int) {
	s.budget.Release(size)
}

// offer hands the units of one reference command to the engine, which keeps them for
// candidate claims. Replies beyond the budget quarantine the candidate.
func (s *Server) offer(ctx context.Context, units []scopedUnit) {
	if !s.offering() {
		return
	}
	for _, unit := range units {
		saved, reserved := reserveReply(s.budget, unit.Reply)
		if !reserved {
			s.exceed(ctx, []scopedUnit{unit})
			return
		}
		saved.name(unit.names)
		decision, err := s.reference.ProcessRequest(ctx, Some(s.scope), unit.Route, None[string](), s.request(unit.pairing))
		forward, ok := decision.(engine.DecisionForward)
		if err != nil || !ok {
			saved.Release()
			logger.FromContext(ctx).ErrorContext(ctx, "Reference unit was not recorded", "route", unit.Route, "error", err)
			continue
		}
		s.reference.ProcessResponse(ctx, forward.Exchange, engine.Response{Raw: saved, Decode: undecoded})
	}
}

// exceed quarantines the candidate for units whose replies are beyond the budget.
func (s *Server) exceed(ctx context.Context, units []scopedUnit) {
	for _, unit := range units {
		s.candidates.Quarantine(ctx, model.QuarantineReason{
			Kind:    model.QuarantineCapacity,
			Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: scripted.Protocol, Route: unit.Route}),
			Detail:  "saved reference replies exceed the MySQL saved replies size",
		})
	}
}

// admit starts a candidate command that stop cancels, unless candidate work is
// refused. finish ends an admitted command.
func (s *Server) admit(ctx context.Context, stop context.CancelFunc) (finish func(), admitted bool) {
	run, ok := s.candidates.Start(ctx, stop).Get()
	if !ok {
		return func() {}, false
	}
	return func() { s.candidates.Finish(run) }, true
}

// claim waits for the reply of an equivalent reference unit, and keeps it in claimed
// until released. The engine quarantines the candidate when there is none.
func (s *Server) claim(ctx context.Context, claimed *[]*savedReply, route model.Route, by pairing) (*savedReply, error) {
	decision, err := s.candidate.ProcessRequest(ctx, Some(s.scope), route, None[string](), s.request(by))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	intercept, ok := decision.(engine.DecisionIntercept)
	if !ok {
		return nil, errors.Errorf("engine decided %T for a candidate unit", decision)
	}
	saved, ok := intercept.Raw.(*savedReply)
	if !ok {
		intercept.Raw.Release()
		return nil, errors.Errorf("engine replayed %T for a candidate unit", intercept.Raw)
	}
	*claimed = append(*claimed, saved)
	return saved, nil
}

// unable quarantines the candidate for a command that cannot be answered, unless
// the proxy stopped the candidate itself.
func (s *Server) unable(ctx context.Context, route model.Route, reason string) {
	if errors.Is(context.Cause(ctx), engine.ErrCandidateStopped) {
		return
	}
	s.candidates.Quarantine(ctx, model.QuarantineReason{
		Kind:    model.QuarantineUnable,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: scripted.Protocol, Route: route}),
		Detail:  reason,
	})
}

// request is the payload egress hashes to pair a unit.
func (s *Server) request(by pairing) func(ctx context.Context) (schema.Value, error) {
	return func(context.Context) (schema.Value, error) {
		declared, err := s.schema.Type(scripted.Pairing)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		data, err := json.Marshal(by)
		if err != nil {
			return nil, errors.Wrap(err, "encode pairing")
		}
		return errors.WithStack2(schema.Decode(declared, data))
	}
}

// undecoded is the content of saved replies, which only replay and are never compared.
func undecoded(context.Context) engine.Content {
	return engine.Content{Failure: Some(errors.New("MySQL replies are not decoded"))}
}

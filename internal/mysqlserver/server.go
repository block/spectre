package mysqlserver

import (
	"context"
	"net"
	"sync"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/client"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/proxy"
	"github.com/block/spectre/internal/schema"
)

// Protocol is the protocol of MySQL servers. Its normaliser names the routes.
const Protocol model.Protocol = "mysql"

// serverVersion is the MySQL version the proxy reports to the services.
const serverVersion = "8.4.0"

// Server serves reference and candidate MySQL connections on separate listeners.
// Candidate statements never reach a database under any condition.
type Server struct {
	config     Config
	wire       *server.Server
	users      *server.InMemoryAuthenticationHandler
	reference  protocol.Acceptor
	candidate  protocol.Acceptor
	candidates *proxy.Candidates
	budget     *proxy.Budget
	normaliser normaliser.Normaliser
	// scope pairs every unit with every other, because statements carry no request ID.
	scope protocol.RequestID
}

// New returns a MySQL proxy that pairs the units of normaliser in engine, and shares
// candidates with the server that owns the engine.
func New(config Config, normaliser normaliser.Normaliser, engine *protocol.Engine, candidates *proxy.Candidates) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !config.Enabled() {
		return nil, errors.New("MySQL upstream is required")
	}
	scope, err := typeid.New[protocol.RequestID]()
	if err != nil {
		return nil, errors.Wrap(err, "name the pairing scope")
	}
	// Native passwords need neither TLS nor a key pair on the loopback listeners.
	users := server.NewInMemoryAuthenticationHandler(mysql.AUTH_NATIVE_PASSWORD)
	if err := users.AddUser(config.User, config.Password); err != nil {
		return nil, errors.Wrap(err, "register the MySQL user")
	}
	return &Server{
		config:     config,
		users:      users,
		wire:       server.NewServer(serverVersion, mysql.DEFAULT_COLLATION_ID, mysql.AUTH_NATIVE_PASSWORD, nil, nil),
		reference:  engine.EgressReference(Protocol),
		candidate:  engine.EgressCandidate(Protocol),
		candidates: candidates,
		budget:     proxy.NewBudget(config.SavedRepliesBytes),
		normaliser: normaliser,
		scope:      scope,
	}, nil
}

// Serve accepts reference and candidate connections until the context is cancelled,
// then closes them and waits for their handlers.
func (s *Server) Serve(ctx context.Context, reference net.Listener, candidate net.Listener) error {
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
			serve(ctx, conn)
		})
	}
}

// serveReference gives a reference connection its own connection to the database.
func (s *Server) serveReference(ctx context.Context, conn net.Conn) {
	upstream, err := client.Connect(s.config.Upstream, s.config.User, s.config.Password, "")
	if err != nil {
		logger.FromContext(ctx).ErrorContext(ctx, "Reference connection has no database", "error", err)
		return
	}
	handler := newReferenceConn(ctx, s, upstream, s.normaliser.NewSession())
	defer handler.close()
	s.serve(ctx, conn, handler)
}

// serveCandidate answers a candidate connection without a database.
func (s *Server) serveCandidate(ctx context.Context, conn net.Conn) {
	s.serve(ctx, conn, newCandidateConn(ctx, s, s.normaliser.NewSession()))
}

// serve runs the wire protocol on a connection until it closes.
func (s *Server) serve(ctx context.Context, conn net.Conn, handler server.Handler) {
	wire, err := s.wire.NewCustomizedConn(conn, s.users, handler)
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

// offer hands the units of one reference command to the engine, which keeps them
// for candidate claims. Replies beyond the budget quarantine the candidate.
func (s *Server) offer(ctx context.Context, units []normaliser.Unit) {
	if s.candidates.QuarantineStatus().Ok() {
		return
	}
	for _, unit := range units {
		saved, reserved := reserveReply(s.budget, unit.Reply)
		if !reserved {
			s.candidates.Quarantine(ctx, model.QuarantineReason{
				Kind:    model.QuarantineCapacity,
				Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: Protocol, Route: unit.Route}),
				Detail:  "saved reference replies exceed the MySQL saved replies size",
			})
			return
		}
		decision, err := s.reference.ProcessRequest(ctx, Some(s.scope), unit.Route, None[string](), request(unit.Key))
		forward, ok := decision.(protocol.DecisionForward)
		if err != nil || !ok {
			saved.Release()
			logger.FromContext(ctx).ErrorContext(ctx, "Reference unit was not recorded", "route", unit.Route, "error", err)
			continue
		}
		s.reference.ProcessResponse(ctx, forward.Exchange, protocol.Response{Raw: saved, Decode: undecoded})
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
func (s *Server) claim(ctx context.Context, claimed *[]*savedReply, route model.Route, key any) (normaliser.Reply, error) { //nolint:ireturn // Replies are a sum type.
	decision, err := s.candidate.ProcessRequest(ctx, Some(s.scope), route, None[string](), request(key))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	intercept, ok := decision.(protocol.DecisionIntercept)
	if !ok {
		return nil, errors.Errorf("engine decided %T for a candidate unit", decision)
	}
	saved, ok := intercept.Raw.(*savedReply)
	if !ok {
		intercept.Raw.Release()
		return nil, errors.Errorf("engine replayed %T for a candidate unit", intercept.Raw)
	}
	*claimed = append(*claimed, saved)
	return saved.value(), nil
}

// unable quarantines the candidate for a command that cannot be answered, unless
// the proxy stopped the candidate itself.
func (s *Server) unable(ctx context.Context, route model.Route, reason string) {
	if errors.Is(context.Cause(ctx), protocol.ErrCandidateStopped) {
		return
	}
	s.candidates.Quarantine(ctx, model.QuarantineReason{
		Kind:    model.QuarantineUnable,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: Protocol, Route: route}),
		Detail:  reason,
	})
}

// request is the payload egress hashes for a unit's key.
func request(key any) func(ctx context.Context) (protocol.Message, error) {
	return func(context.Context) (protocol.Message, error) {
		return protocol.Message{Type: None[schema.TypeRef](), Value: key}, nil
	}
}

// undecoded is the content of saved replies, which only replay and are never compared.
func undecoded(context.Context) protocol.Content {
	return protocol.Content{Failure: Some(errors.New("MySQL replies are not decoded"))}
}

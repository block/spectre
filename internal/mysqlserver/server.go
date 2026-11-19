package mysqlserver

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/client"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/proxy"
)

// Protocol is the protocol of MySQL servers.
const Protocol model.Protocol = "mysql"

// dialTimeout limits connecting to the database when a reference connection authenticates.
const dialTimeout = 10 * time.Second

// serverVersion is the MySQL version the proxy reports to the services.
const serverVersion = "8.4.0"

// Server serves reference and candidate MySQL connections on separate listeners.
// Candidate statements never reach a database under any condition.
type Server struct {
	// upstream is the database that reference connections reach.
	upstream string
	// user and passwordFile are the proxy's credentials for the database.
	user         string
	passwordFile string
	// candidateWire accepts any candidate credentials. Reference connections each get their own.
	candidateWire *server.Server
	upstreamTLS   Option[*tls.Config]
	candidates    *proxy.Candidates
}

// New returns a MySQL proxy that quarantines candidates as the server that owns
// candidates.
func New(config Config, candidates *proxy.Candidates) (*Server, error) {
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
	return &Server{
		upstream:      config.Upstream,
		user:          config.UpstreamUser,
		passwordFile:  config.UpstreamPasswordFile,
		candidateWire: newWire(acceptAny{}),
		upstreamTLS:   upstreamTLS,
		candidates:    candidates,
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

// newWire returns the wire protocol of connections that provider authenticates.
// Native passwords need neither TLS nor a key pair on the local listeners.
func newWire(provider server.AuthenticationProvider) *server.Server {
	return server.NewServerWithAuth(serverVersion, mysql.DEFAULT_COLLATION_ID, mysql.AUTH_NATIVE_PASSWORD, nil, nil, provider)
}

// serveReference accepts a reference connection with any credentials, and connects
// to the database with the proxy's own.
func (s *Server) serveReference(ctx context.Context, conn net.Conn) {
	handler := newReferenceConn(ctx, s)
	defer handler.close()
	s.serve(ctx, conn, newWire(handler), anyUser{}, handler)
}

// connect connects to the database with the proxy's credentials, and selects database
// if one is named. The password file is read for every connection, so it can be rotated.
func (s *Server) connect(ctx context.Context, database string) (*client.Conn, error) {
	password, err := os.ReadFile(s.passwordFile)
	if err != nil {
		return nil, errors.Wrap(err, "read the MySQL password file")
	}
	options := []client.Option{}
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

// serveCandidate answers a candidate connection without a database.
func (s *Server) serveCandidate(ctx context.Context, conn net.Conn) {
	handler := newCandidateConn(ctx, s)
	s.serve(ctx, conn, s.candidateWire, anyUser{}, handler)
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

// unable quarantines the candidate for a command that cannot be answered.
func (s *Server) unable(ctx context.Context, route model.Route, reason string) {
	s.candidates.Quarantine(ctx, model.QuarantineReason{
		Kind:    model.QuarantineUnable,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: Protocol, Route: route}),
		Detail:  reason,
	})
}

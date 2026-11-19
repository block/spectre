package mysqlserver

import (
	"context"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
	"github.com/block/spectre/internal/protocol"
)

// unsupportedRoute names candidate commands that the proxy does not hand to the normaliser.
const unsupportedRoute = model.Route("command")

// candidateConn answers one candidate connection's commands from reference replies
// that its normaliser claims. It has no connection to a database.
type candidateConn struct {
	ctx     context.Context //nolint:containedctx // The wire library's handler methods take no context.
	server  *Server
	session normaliser.Session
}

func newCandidateConn(ctx context.Context, server *Server, session normaliser.Session) *candidateConn {
	return &candidateConn{ctx: ctx, server: server, session: session}
}

// UseDB selects the default database through the normaliser. It needs no reference
// reply, so it is not admitted as candidate work.
func (c *candidateConn) UseDB(database string) error {
	_, err := c.session.Candidate(c.ctx, normaliser.UseDatabase{Name: strings.Clone(database)}, unclaimed)
	if unsupported, ok := errors.AsType[normaliser.UnsupportedError](err); ok {
		c.server.unable(c.ctx, unsupported.Route, unsupported.Reason)
		return refusal(unsupported.Reason)
	}
	return errors.WithStack(err)
}

// HandleQuery answers a statement through the normaliser. One that cannot be
// answered quarantines the candidate.
func (c *candidateConn) HandleQuery(query string) (*mysql.Result, error) {
	// The statement outlives the wire library's buffer, as do the parts parsed from it.
	return c.answer(c.server.command(mysql.COM_QUERY, strings.Clone(query)))
}

// HandleFieldList is refused, because it reads the database's schema.
func (c *candidateConn) HandleFieldList(_ string, _ string) ([]*mysql.Field, error) {
	return nil, c.unsupported("column listings are not supported")
}

// HandleStmtPrepare is refused, because only text statements are answered.
func (c *candidateConn) HandleStmtPrepare(_ string) (params int, columns int, prepared any, err error) {
	return 0, 0, nil, c.unsupported("prepared statements are not supported")
}

// HandleStmtExecute is never reached, because no statement is prepared.
func (c *candidateConn) HandleStmtExecute(_ any, _ string, _ []any) (*mysql.Result, error) {
	return nil, c.unsupported("prepared statements are not supported")
}

// HandleStmtClose has nothing to close.
func (c *candidateConn) HandleStmtClose(_ any) error {
	return nil
}

// HandleOtherCommand refuses every other command.
func (c *candidateConn) HandleOtherCommand(_ byte, _ []byte) error {
	return c.unsupported("command is not supported")
}

// answer runs a command as admitted candidate work, and replies with what the
// normaliser claimed. Claimed replies are released once converted.
func (c *candidateConn) answer(command normaliser.Command) (*mysql.Result, error) {
	ctx, cancel := context.WithCancelCause(c.ctx)
	defer cancel(nil)
	finish, admitted := c.server.admit(ctx, func() { cancel(protocol.ErrCandidateStopped) })
	if !admitted {
		return nil, refusal("candidate commands are refused")
	}
	defer finish()
	claimed := []*savedReply{}
	defer func() {
		for _, saved := range claimed {
			saved.Release()
		}
	}()
	reply, err := c.session.Candidate(ctx, command, func(ctx context.Context, route model.Route, key any) (normaliser.Reply, error) {
		return c.server.claim(ctx, &claimed, route, key)
	})
	if unsupported, ok := errors.AsType[normaliser.UnsupportedError](err); ok {
		c.server.unable(ctx, unsupported.Route, unsupported.Reason)
		return nil, refusal(unsupported.Reason)
	}
	if err != nil {
		return nil, refusal("no matching reference reply")
	}
	return resultOf(reply)
}

// unsupported quarantines the candidate for a command the proxy cannot answer.
func (c *candidateConn) unsupported(reason string) error {
	c.server.unable(c.ctx, unsupportedRoute, reason)
	return refusal(reason)
}

// refusal is the error a candidate command gets instead of a reply.
func refusal(reason string) error {
	return mysql.NewError(mysql.ER_UNKNOWN_ERROR, "SPECTRE: "+reason)
}

// unclaimed is the claim of a command that needs no reference reply.
func unclaimed(_ context.Context, route model.Route, _ any) (normaliser.Reply, error) {
	return nil, errors.Errorf("%s needs no reference reply", route)
}

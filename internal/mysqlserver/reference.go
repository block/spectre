package mysqlserver

import (
	"context"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/go-mysql-org/go-mysql/client"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

// referenceConn forwards one reference connection's commands to the database on a
// connection of its own, and offers the replies of text commands to the candidate.
type referenceConn struct {
	ctx      context.Context //nolint:containedctx // The wire library's handler methods take no context.
	server   *Server
	upstream *client.Conn
	session  normaliser.Session
}

func newReferenceConn(ctx context.Context, server *Server, upstream *client.Conn, session normaliser.Session) *referenceConn {
	return &referenceConn{ctx: ctx, server: server, upstream: upstream, session: session}
}

// UseDB selects the database on the connection to the database server.
func (r *referenceConn) UseDB(database string) error {
	command := normaliser.UseDatabase{Name: strings.Clone(database)}
	if err := r.upstream.UseDB(database); err != nil {
		r.offer(command)(errorReply(err))
		return databaseError(err)
	}
	r.offer(command)(normaliser.OK{}, true)
	return nil
}

// HandleQuery forwards a statement and returns the database's own answer. Offering
// it to the candidate never changes that answer.
func (r *referenceConn) HandleQuery(query string) (*mysql.Result, error) {
	command := normaliser.Query{SQL: strings.Clone(query)}
	result, err := r.upstream.Execute(query)
	if err != nil {
		r.offer(command)(errorReply(err))
		return nil, databaseError(err)
	}
	r.offer(command)(resultReply(result))
	return result, nil
}

// HandleFieldList forwards a column listing.
func (r *referenceConn) HandleFieldList(table string, wildcard string) ([]*mysql.Field, error) {
	fields, err := r.upstream.FieldList(table, wildcard)
	if err != nil {
		return nil, databaseError(err)
	}
	return fields, nil
}

// HandleStmtPrepare prepares a statement on the database. Prepared statements are
// only forwarded.
func (r *referenceConn) HandleStmtPrepare(query string) (params int, columns int, prepared any, err error) {
	statement, err := r.upstream.Prepare(query)
	if err != nil {
		return 0, 0, nil, databaseError(err)
	}
	return statement.ParamNum(), statement.ColumnNum(), statement, nil
}

// HandleStmtExecute runs a prepared statement on the database.
func (r *referenceConn) HandleStmtExecute(prepared any, _ string, args []any) (*mysql.Result, error) {
	statement, ok := prepared.(*client.Stmt)
	if !ok {
		return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR, "statement was not prepared on this connection")
	}
	result, err := statement.Execute(args...)
	if err != nil {
		return nil, databaseError(err)
	}
	return result, nil
}

// HandleStmtClose closes a prepared statement on the database.
func (r *referenceConn) HandleStmtClose(prepared any) error {
	statement, ok := prepared.(*client.Stmt)
	if !ok {
		return nil
	}
	return databaseError(statement.Close())
}

// HandleOtherCommand refuses commands that the proxy cannot forward.
func (r *referenceConn) HandleOtherCommand(_ byte, _ []byte) error {
	return mysql.NewError(mysql.ER_NOT_SUPPORTED_YET, "the SPECTRE MySQL proxy does not support this command")
}

// offer returns what hands the units of a command and its reply to the server. A
// failure that is not the database's answer is not offered.
func (r *referenceConn) offer(command normaliser.Command) func(reply normaliser.Reply, answered bool) {
	return func(reply normaliser.Reply, answered bool) {
		if !answered {
			return
		}
		units, err := r.session.Reference(r.ctx, command, reply)
		if err != nil {
			logger.FromContext(r.ctx).WarnContext(r.ctx, "Reference command is not offered", "event", logger.EventCorrelation, "reason", err)
			return
		}
		r.server.offer(r.ctx, units)
	}
}

// close closes the connection to the database server.
func (r *referenceConn) close() {
	_ = r.upstream.Close()
}

// databaseError returns the database's own error when there is one, so the wire
// library sends the reference the code the database sent.
func databaseError(err error) error {
	if err == nil {
		return nil
	}
	if reported, ok := errors.AsType[*mysql.MyError](err); ok {
		return reported
	}
	return errors.WithStack(err)
}

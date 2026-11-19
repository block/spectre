package mysqlserver

import (
	"context"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/client"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

// referenceConn forwards one reference connection's commands to the database on a
// connection of its own, and offers the replies of text commands to the candidate.
type referenceConn struct {
	ctx    context.Context //nolint:containedctx // The wire library's handler methods take no context.
	server *Server
	// upstream is connected when the reference authenticates, with the proxy's credentials.
	upstream Option[*client.Conn]
	// database is the one the reference selected while connecting.
	database string
	session  normaliser.Session
}

func newReferenceConn(ctx context.Context, server *Server, session normaliser.Session) *referenceConn {
	return &referenceConn{ctx: ctx, server: server, session: session}
}

// Validate supports native passwords, which need neither TLS nor a key pair.
func (r *referenceConn) Validate(plugin string) bool {
	return plugin == mysql.AUTH_NATIVE_PASSWORD
}

// Authenticate accepts any credentials, and connects to the database with the
// proxy's own. A refusal is the database's own error.
func (r *referenceConn) Authenticate(_ *server.Conn, _ string, _ []byte) error {
	upstream, err := r.server.connect(r.ctx, r.database)
	if err != nil {
		logger.FromContext(r.ctx).WarnContext(r.ctx, "Reference connection has no database", "error", err)
		if reported, ok := errors.AsType[*mysql.MyError](err); ok {
			return reported
		}
		return mysql.NewError(mysql.ER_UNKNOWN_ERROR, "SPECTRE: the proxy cannot connect to the database")
	}
	r.upstream = Some(upstream)
	if r.database != "" {
		r.offer(normaliser.UseDatabase{Name: r.database})(normaliser.OK{}, true)
	}
	return nil
}

// UseDB selects the database on the connection to the database server. A database
// selected while connecting is selected once the reference authenticates.
func (r *referenceConn) UseDB(database string) error {
	upstream, ok := r.upstream.Get()
	if !ok {
		r.database = strings.Clone(database)
		return nil
	}
	command := normaliser.UseDatabase{Name: strings.Clone(database)}
	if err := upstream.UseDB(database); err != nil {
		r.offer(command)(errorReply(err))
		return databaseError(err)
	}
	r.offer(command)(normaliser.OK{}, true)
	return nil
}

// HandleQuery forwards a statement and returns the database's own answer. Offering
// it to the candidate never changes that answer.
func (r *referenceConn) HandleQuery(query string) (*mysql.Result, error) {
	upstream, err := r.connection()
	if err != nil {
		return nil, err
	}
	command := normaliser.Query{SQL: strings.Clone(query)}
	result, err := upstream.Execute(query)
	if err != nil {
		r.offer(command)(errorReply(err))
		return nil, databaseError(err)
	}
	r.offer(command)(resultReply(result))
	return result, nil
}

// HandleFieldList forwards a column listing.
func (r *referenceConn) HandleFieldList(table string, wildcard string) ([]*mysql.Field, error) {
	upstream, err := r.connection()
	if err != nil {
		return nil, err
	}
	fields, err := upstream.FieldList(table, wildcard)
	if err != nil {
		return nil, databaseError(err)
	}
	return fields, nil
}

// HandleStmtPrepare prepares a statement on the database. Prepared statements are
// only forwarded.
func (r *referenceConn) HandleStmtPrepare(query string) (params int, columns int, prepared any, err error) {
	upstream, err := r.connection()
	if err != nil {
		return 0, 0, nil, err
	}
	statement, err := upstream.Prepare(query)
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

// connection is the connection to the database that authentication opened.
func (r *referenceConn) connection() (*client.Conn, error) {
	upstream, ok := r.upstream.Get()
	if !ok {
		return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR, "SPECTRE: the connection is not authenticated")
	}
	return upstream, nil
}

// close closes the connection to the database server, if there is one.
func (r *referenceConn) close() {
	if upstream, ok := r.upstream.Get(); ok {
		_ = upstream.Close()
	}
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

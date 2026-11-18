package mysqlserver

import (
	"context"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/go-mysql-org/go-mysql/client"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/pingcap/tidb/pkg/parser"

	"github.com/block/spectre/internal/logger"
)

// referenceConn forwards one reference connection's statements to the database on a
// connection of its own, and saves the rows of the lookups they stand for.
type referenceConn struct {
	ctx      context.Context //nolint:containedctx // The wire library's handler methods take no context.
	server   *Server
	upstream *client.Conn
	parser   *parser.Parser
	database string
}

func newReferenceConn(ctx context.Context, server *Server, upstream *client.Conn) *referenceConn {
	return &referenceConn{ctx: ctx, server: server, upstream: upstream, parser: parser.New()}
}

// UseDB selects the database on the connection to the database server.
func (r *referenceConn) UseDB(database string) error {
	if err := r.upstream.UseDB(database); err != nil {
		return databaseError(err)
	}
	r.database = strings.Clone(database)
	return nil
}

// HandleQuery forwards a statement and returns the database's own answer. Saving
// its rows never changes that answer.
func (r *referenceConn) HandleQuery(query string) (*mysql.Result, error) {
	result, err := r.upstream.Execute(query)
	if err != nil {
		return nil, databaseError(err)
	}
	if result.HasResultset() {
		r.save(strings.Clone(query), result.Resultset)
	}
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

// HandleStmtPrepare prepares a statement on the database. Its rows are not saved.
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

// save offers the lookups a statement stands for to candidate statements. A statement
// that lookups cannot express is only forwarded.
func (r *referenceConn) save(query string, result *mysql.Resultset) {
	log := logger.FromContext(r.ctx)
	selected, err := parseSelection(r.parser, query, r.database)
	if err != nil {
		log.DebugContext(r.ctx, "Reference statement is not saved", "event", logger.EventCorrelation, "reason", err)
		return
	}
	lookups, err := split(selected, result, r.server.primaryKey)
	if err != nil {
		log.WarnContext(r.ctx, "Reference statement is not saved", "event", logger.EventCorrelation,
			"route", newLookup(selected.driving, selected.where).route(), "reason", err)
		return
	}
	r.server.offer(r.ctx, lookups)
}

// close closes the connection to the database server.
func (r *referenceConn) close() {
	_ = r.upstream.Close()
}

// databaseError returns the database's own error when there is one, so the wire
// library sends the reference the code the database sent.
func databaseError(err error) error {
	if reported, ok := errors.AsType[*mysql.MyError](err); ok {
		return reported
	}
	return errors.WithStack(err)
}

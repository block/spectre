package mysqlserver

import (
	"context"
	"strings"

	"github.com/alecthomas/errors"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/pingcap/tidb/pkg/parser"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocol"
)

// unsupportedRoute names candidate statements that read no table the proxy can name.
const unsupportedRoute = model.Route("statement")

// candidateConn answers one candidate connection's statements from saved reference
// rows. It has no connection to a database.
type candidateConn struct {
	ctx      context.Context //nolint:containedctx // The wire library's handler methods take no context.
	server   *Server
	parser   *parser.Parser
	database string
}

func newCandidateConn(ctx context.Context, server *Server) *candidateConn {
	return &candidateConn{ctx: ctx, server: server, parser: parser.New()}
}

// UseDB remembers the database that unqualified tables are in.
func (c *candidateConn) UseDB(database string) error {
	c.database = strings.Clone(database)
	return nil
}

// HandleQuery answers a statement from the reference rows of its lookups. One that
// cannot be answered quarantines the candidate.
func (c *candidateConn) HandleQuery(query string) (*mysql.Result, error) {
	ctx, cancel := context.WithCancelCause(c.ctx)
	defer cancel(nil)
	finish, admitted := c.server.admit(ctx, func() { cancel(protocol.ErrCandidateStopped) })
	if !admitted {
		return nil, refusal("candidate statements are refused")
	}
	defer finish()
	selected, err := parseSelection(c.parser, strings.Clone(query), c.database)
	if err != nil {
		c.server.unable(ctx, unsupportedRoute, err)
		return nil, refusal("statement is not supported")
	}
	result, err := c.answer(ctx, selected)
	if err != nil {
		return nil, refusal("no matching reference rows")
	}
	return result, nil
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

// answer claims the statement's lookups in the order a nested loop reads them, and
// builds its result from their rows.
func (c *candidateConn) answer(ctx context.Context, selected selection) (*mysql.Result, error) {
	wanted := newLookup(selected.driving, selected.where)
	route := wanted.route()
	claimed, err := c.server.claim(ctx, wanted)
	if err != nil {
		return nil, err
	}
	defer claimed.Release()
	driving := claimed.table()
	read := newEvaluation(selected, driving.fields)
	joined, hasJoin := selected.join.Get()
	if _, known := driving.fields[joined.drivingColumn]; hasJoin && !known {
		err := errors.New("statement joins on a column that the reference did not read")
		c.server.unable(ctx, route, err)
		return nil, err
	}
	for _, drivingRow := range driving.rows {
		value, joins := drivingRow[joined.drivingColumn].Get()
		if !hasJoin || !joins {
			read.add(drivingRow, nil)
			continue
		}
		match, err := c.server.claim(ctx, joinedLookup(joined, value))
		if err != nil {
			return nil, err
		}
		matched := match.table()
		read.read(matched.fields)
		read.add(drivingRow, matched.rows)
		match.Release()
	}
	result, err := read.result()
	if err != nil {
		c.server.unable(ctx, route, err)
		return nil, err
	}
	return result, nil
}

// unsupported quarantines the candidate for something the proxy cannot answer.
func (c *candidateConn) unsupported(reason string) error {
	c.server.unable(c.ctx, unsupportedRoute, errors.New(reason))
	return refusal(reason)
}

// refusal is the error a candidate statement gets instead of rows.
func refusal(reason string) error {
	return mysql.NewError(mysql.ER_UNKNOWN_ERROR, "SPECTRE: "+reason)
}

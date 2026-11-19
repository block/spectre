package mysqlserver

import (
	"context"

	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/model"
)

// candidateRoute names every candidate command, none of which the proxy answers yet.
const candidateRoute = model.Route("command")

// candidateConn refuses one candidate connection's commands. It has no connection to
// a database.
type candidateConn struct {
	ctx    context.Context //nolint:containedctx // The wire library's handler methods take no context.
	server *Server
}

func newCandidateConn(ctx context.Context, server *Server) *candidateConn {
	return &candidateConn{ctx: ctx, server: server}
}

// UseDB accepts any database, so the candidate connects and is quarantined only by
// the commands it sends.
func (c *candidateConn) UseDB(_ string) error {
	return nil
}

// HandleQuery is refused.
func (c *candidateConn) HandleQuery(_ string) (*mysql.Result, error) {
	return nil, c.unsupported("statements are not supported")
}

// HandleFieldList is refused.
func (c *candidateConn) HandleFieldList(_ string, _ string) ([]*mysql.Field, error) {
	return nil, c.unsupported("column listings are not supported")
}

// HandleStmtPrepare is refused.
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

// unsupported quarantines the candidate for a command the proxy cannot answer.
func (c *candidateConn) unsupported(reason string) error {
	c.server.unable(c.ctx, candidateRoute, reason)
	return mysql.NewError(mysql.ER_UNKNOWN_ERROR, "SPECTRE: "+reason)
}

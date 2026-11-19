// Package exact pairs MySQL statements that are the same once parsed, in the same
// default database. Comments, spacing, keyword case and quoting do not matter, but
// every value does. A candidate statement replays the whole reply of the reference
// statement it pairs with, so identical writes and transaction statements are
// answered without the database.
package exact

import (
	"context"
	"strings"
	"sync"
	"unicode"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver" // Literal values for the parser, without the rest of TiDB.

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

// statementRoute names statements that start with no keyword.
const statementRoute = model.Route("statement")

// Normaliser pairs statements that parse to the same text in the same default database.
type Normaliser struct {
	// parsers are reused, because each holds buffers and none can parse concurrently.
	parsers *sync.Pool
}

// New returns a normaliser that pairs equal statements.
func New() Normaliser {
	return Normaliser{parsers: &sync.Pool{New: func() any { return parser.New() }}}
}

// NewSession starts a session with no default database.
func (n Normaliser) NewSession() normaliser.Session { //nolint:ireturn // Sessions are the contract's interface.
	return newSession(n.parsers)
}

// session tracks the default database of one connection.
type session struct {
	parsers  *sync.Pool
	database string
}

func newSession(parsers *sync.Pool) *session {
	return &session{parsers: parsers}
}

// Reference offers a statement's reply under its text and default database. Selecting
// a database offers nothing, and changes the database once the server accepts it.
func (s *session) Reference(_ context.Context, command normaliser.Command, reply normaliser.Reply) ([]normaliser.Unit, error) {
	text, database, selects := s.parse(command)
	if selects {
		if _, ok := reply.(normaliser.OK); ok {
			s.database = database
		}
		return nil, nil
	}
	if _, ok := command.(normaliser.Query); !ok {
		return nil, nil
	}
	return []normaliser.Unit{{Route: route(text), Key: s.key(text), Reply: reply}}, nil
}

// Candidate replays the reply of the same reference statement. Selecting a database
// is answered without one.
func (s *session) Candidate(ctx context.Context, command normaliser.Command, claim normaliser.Claim,
) (normaliser.Reply, error) { //nolint:ireturn // Replies are the contract's sum type.
	text, database, selects := s.parse(command)
	if selects {
		s.database = database
		return normaliser.OK{}, nil
	}
	if _, ok := command.(normaliser.Query); ok {
		return claim(ctx, route(text), s.key(text))
	}
	return nil, normaliser.UnsupportedError{Route: statementRoute, Reason: "command is not supported"}
}

// parse returns the text a statement pairs by, or the database it selects. A
// statement that does not parse pairs by the text it was sent as.
func (s *session) parse(command normaliser.Command) (text, database string, selects bool) {
	switch command := command.(type) {
	case normaliser.UseDatabase:
		return "", command.Name, true
	case normaliser.Query:
		nodes, parsed := s.statements(command.SQL)
		if !parsed {
			return command.SQL, "", false
		}
		if use, ok := nodes[0].(*ast.UseStmt); ok && len(nodes) == 1 {
			return "", use.DBName, true
		}
		restored := make([]string, 0, len(nodes))
		for _, node := range nodes {
			var text strings.Builder
			if err := node.Restore(format.NewRestoreCtx(format.DefaultRestoreFlags, &text)); err != nil {
				return command.SQL, "", false
			}
			restored = append(restored, text.String())
		}
		return strings.Join(restored, "; "), "", false
	}
	return "", "", false
}

// statements parses sql with a pooled parser.
func (s *session) statements(sql string) ([]ast.StmtNode, bool) {
	sqlParser, ok := s.parsers.Get().(*parser.Parser)
	if !ok {
		sqlParser = parser.New()
	}
	defer s.parsers.Put(sqlParser)
	nodes, _, err := sqlParser.Parse(sql, "", "")
	if err != nil || len(nodes) == 0 {
		return nil, false
	}
	return nodes, true
}

// key is what pairs a statement: its text, and the database that unqualified names are in.
func (s *session) key(text string) map[string]any {
	return map[string]any{"database": s.database, "statement": text}
}

// route is the statement's leading keyword in upper case, which names it without values.
func route(sql string) model.Route {
	trimmed := strings.TrimLeftFunc(sql, func(r rune) bool { return unicode.IsSpace(r) || r == '(' })
	end := strings.IndexFunc(trimmed, func(r rune) bool { return !unicode.IsLetter(r) })
	if end < 0 {
		end = len(trimmed)
	}
	if end == 0 {
		return statementRoute
	}
	return model.Route(strings.ToUpper(trimmed[:end]))
}

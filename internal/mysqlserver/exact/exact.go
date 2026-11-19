// Package exact pairs MySQL statements by their text without comments and their
// default database. A candidate statement replays the whole reply of the same
// reference statement, so identical writes and transaction statements are answered
// without the database.
package exact

import (
	"context"
	"strings"
	"unicode"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

// statementRoute names statements that start with no keyword.
const statementRoute = model.Route("statement")

// Normaliser pairs statements of the same text and default database.
type Normaliser struct{}

// New returns a normaliser that pairs identical statements.
func New() Normaliser {
	return Normaliser{}
}

// NewSession starts a session with no default database.
func (Normaliser) NewSession() normaliser.Session { //nolint:ireturn // Sessions are the contract's interface.
	return newSession()
}

// session tracks the default database of one connection.
type session struct {
	database string
}

func newSession() *session {
	return &session{}
}

// Reference offers a statement's reply under its text and default database.
func (s *session) Reference(_ context.Context, command normaliser.Command, reply normaliser.Reply) ([]normaliser.Unit, error) {
	switch command := command.(type) {
	case normaliser.Query:
		statement := withoutComments(command.SQL)
		return []normaliser.Unit{{Route: route(statement), Key: s.key(statement), Reply: reply}}, nil
	case normaliser.UseDatabase:
		if _, ok := reply.(normaliser.OK); ok {
			s.database = command.Name
		}
		return nil, nil
	}
	return nil, nil
}

// Candidate replays the reply of the same reference statement.
func (s *session) Candidate(ctx context.Context, command normaliser.Command, claim normaliser.Claim,
) (normaliser.Reply, error) { //nolint:ireturn // Replies are the contract's sum type.
	switch command := command.(type) {
	case normaliser.Query:
		statement := withoutComments(command.SQL)
		return claim(ctx, route(statement), s.key(statement))
	case normaliser.UseDatabase:
		s.database = command.Name
		return normaliser.OK{}, nil
	}
	return nil, normaliser.UnsupportedError{Route: statementRoute, Reason: "command is not supported"}
}

// key is what pairs a statement: its text without comments, which carry request
// tags, and the database that unqualified names are in.
func (s *session) key(sql string) map[string]any {
	return map[string]any{"database": s.database, "statement": sql}
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

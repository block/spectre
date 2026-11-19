// Package exact pairs MySQL statements by their exact text and default database. A
// candidate statement replays the whole reply of the same reference statement, so
// identical writes and transaction statements are answered without the database.
package exact

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

// statementRoute names statements that start with no keyword.
const statementRoute = model.Route("statement")

// Normaliser pairs statements of the same text and default database.
type Normaliser struct {
	use *regexp.Regexp
}

// New returns a normaliser that pairs identical statements.
func New() Normaliser {
	return Normaliser{use: regexp.MustCompile("(?is)^\\s*USE\\s+(`(?:[^`]|``)+`|[^\\s;`]+)\\s*;?\\s*$")}
}

// NewSession starts a session with no default database.
func (n Normaliser) NewSession() normaliser.Session { //nolint:ireturn // Sessions are the contract's interface.
	return newSession(n.use)
}

// session tracks the default database of one connection.
type session struct {
	use      *regexp.Regexp
	database string
}

func newSession(use *regexp.Regexp) *session {
	return &session{use: use}
}

// Reference offers a statement's reply under its text and default database. Selecting
// a database offers nothing, and changes the database once the server accepts it.
func (s *session) Reference(_ context.Context, command normaliser.Command, reply normaliser.Reply) ([]normaliser.Unit, error) {
	database, selects := s.selected(command)
	if selects {
		if _, ok := reply.(normaliser.OK); ok {
			s.database = database
		}
		return nil, nil
	}
	query, ok := command.(normaliser.Query)
	if !ok {
		return nil, nil
	}
	return []normaliser.Unit{{Route: route(query.SQL), Key: s.key(query.SQL), Reply: reply}}, nil
}

// Candidate replays the reply of the same reference statement. Selecting a database
// is answered without one.
func (s *session) Candidate(ctx context.Context, command normaliser.Command, claim normaliser.Claim,
) (normaliser.Reply, error) { //nolint:ireturn // Replies are the contract's sum type.
	if database, selects := s.selected(command); selects {
		s.database = database
		return normaliser.OK{}, nil
	}
	if query, ok := command.(normaliser.Query); ok {
		return claim(ctx, route(query.SQL), s.key(query.SQL))
	}
	return nil, normaliser.UnsupportedError{Route: statementRoute, Reason: "command is not supported"}
}

// selected returns the database that a command selects, by command or USE statement.
func (s *session) selected(command normaliser.Command) (database string, selects bool) {
	switch command := command.(type) {
	case normaliser.UseDatabase:
		return command.Name, true
	case normaliser.Query:
		match := s.use.FindStringSubmatch(command.SQL)
		if match == nil {
			return "", false
		}
		if quoted, ok := strings.CutPrefix(match[1], "`"); ok {
			return strings.ReplaceAll(strings.TrimSuffix(quoted, "`"), "``", "`"), true
		}
		return match[1], true
	}
	return "", false
}

// key is what pairs a statement: its text, and the database that unqualified names are in.
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

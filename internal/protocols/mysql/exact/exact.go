// Package exact pairs MySQL statements that are the same once parsed, in the same database.
//
// Comments, spacing, keyword case and quoting do not matter, but every value does. A candidate
// statement replays its reference statement's whole reply, without the database.
package exact

import (
	"context"
	"strings"
	"unicode"

	"github.com/pingcap/tidb/pkg/parser/format"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/mysql/normaliser"
)

// statementRoute names statements that start with no keyword.
const statementRoute = model.Route("statement")

// Normaliser pairs statements that parse to the same text in the same default database.
type Normaliser struct{}

// New returns a normaliser that pairs equal statements.
func New() Normaliser {
	return Normaliser{}
}

// NewSession starts a session with no default database.
func (n Normaliser) NewSession() normaliser.Session { //nolint:ireturn // Sessions are the contract's interface.
	return newSession()
}

// session tracks the default database of one connection.
type session struct {
	database string
}

func newSession() *session {
	return &session{}
}

// Reference offers a statement's reply under its text and default database. Selecting
// a database offers nothing, and changes the database once the server accepts it.
func (s *session) Reference(_ context.Context, command normaliser.Command, reply normaliser.Reply) ([]normaliser.Unit, error) {
	text, database, selects := pairing(command)
	if selects {
		if _, ok := reply.(normaliser.OK); ok {
			s.database = database
		}
		return nil, nil
	}
	return []normaliser.Unit{{Route: route(text), Key: s.key(text), Reply: reply}}, nil
}

// Candidate replays the reply of the same reference statement. Selecting a database
// is answered without one.
func (s *session) Candidate(ctx context.Context, command normaliser.Command, claim normaliser.Claim,
) (normaliser.Reply, error) { //nolint:ireturn // Replies are the contract's sum type.
	text, database, selects := pairing(command)
	if selects {
		s.database = database
		return normaliser.OK{}, nil
	}
	return claim(ctx, route(text), s.key(text))
}

// pairing returns the text a statement pairs by, or the database it selects. A
// statement that does not parse pairs by the text it was sent as.
func pairing(command normaliser.Command) (text, database string, selects bool) {
	if selected, ok := normaliser.SelectedDatabase(command); ok {
		return "", selected, true
	}
	switch command := command.(type) {
	case normaliser.UseDatabase:
	case normaliser.UnparsedQuery:
		return command.SQL, "", false
	case normaliser.Query:
		restored := make([]string, 0, len(command.Statements))
		for _, statement := range command.Statements {
			var text strings.Builder
			if err := statement.Restore(format.NewRestoreCtx(format.DefaultRestoreFlags, &text)); err != nil {
				return command.SQL, "", false
			}
			restored = append(restored, text.String())
		}
		return strings.Join(restored, "; "), "", false
	}
	return "", "", false
}

// key is what pairs a statement: its text, and the database that unqualified names are in.
func (s *session) key(text string) normaliser.Key {
	return normaliser.Key{Database: s.database, Statement: text}
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

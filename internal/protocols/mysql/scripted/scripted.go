// Package scripted pairs MySQL statements that scripts declare replace each other.
//
// A script combines reference replies into one candidate reply, or splits one into several. Other
// statements pair by pattern and values, or by the fallback. It also defines MySQL's script binding.
package scripted

import (
	"context"
	"slices"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/pingcap/tidb/pkg/parser/ast"

	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/mysql/normaliser"
)

// route names the units of statements that match patterns.
const route = model.Route("SELECT")

// Normaliser pairs statements by the scripts' replacements, and others by its fallback.
type Normaliser struct {
	runner   runner
	rules    []rule
	fallback normaliser.Normaliser
}

// rule is a statement replacement with its patterns read.
type rule struct {
	replacement Replacement
	statements  []pattern
	sources     []pattern
}

// New reads the patterns of the statement replacements that scripts declared through the handle's binding.
func New(scripts *comparison.Scripts, handle Handle, fallback normaliser.Normaliser) (*Normaliser, error) {
	parser := normaliser.NewParser()
	read := func(texts []string) ([]pattern, error) {
		patterns := make([]pattern, 0, len(texts))
		for _, text := range texts {
			read, err := newPattern(parser, text)
			if err != nil {
				return nil, err
			}
			patterns = append(patterns, read)
		}
		return patterns, nil
	}
	run := newRunner(scripts, handle)
	rules := []rule{}
	for _, replacement := range run.replacements() {
		statements, err := read(replacement.Statements())
		if err != nil {
			return nil, errors.Wrapf(err, "read %s", replacement)
		}
		sources, err := read(replacement.Sources())
		if err != nil {
			return nil, errors.Wrapf(err, "read %s", replacement)
		}
		rules = append(rules, rule{replacement: replacement, statements: statements, sources: sources})
	}
	return &Normaliser{runner: run, rules: rules, fallback: fallback}, nil
}

// NewSession starts a session with no default database.
func (n *Normaliser) NewSession() normaliser.Session { //nolint:ireturn // Sessions are the contract's interface.
	return newSession(n.runner, n.rules, n.fallback.NewSession())
}

// session tracks the default database of one connection, as its fallback session does.
type session struct {
	runner   runner
	rules    []rule
	fallback normaliser.Session
	database string
}

func newSession(runner runner, rules []rule, fallback normaliser.Session) *session {
	return &session{runner: runner, rules: rules, fallback: fallback}
}

// Reference splits the reply of a statement that several candidate statements replace,
// and offers that of any other statement that matches a pattern under its pattern.
func (s *session) Reference(ctx context.Context, command normaliser.Command, reply normaliser.Reply) ([]normaliser.Unit, error) {
	if database, selects := normaliser.SelectedDatabase(command); selects {
		if _, ok := reply.(normaliser.OK); ok {
			s.database = database
		}
	}
	if matched, ok := s.match(command); ok {
		for _, rule := range s.rules {
			if !rule.replacement.Combines() && rule.sources[0].matches(matched.digest) {
				return s.split(ctx, rule, matched.values, reply)
			}
			if found, named := patternOf(rule, matched.digest); named {
				return []normaliser.Unit{{Route: route, Key: found.key(s.database, matched.values), Reply: reply}}, nil
			}
		}
	}
	return errors.WithStack2(s.fallback.Reference(ctx, command, reply))
}

// split offers the replies that a script splits a reference statement's reply into.
func (s *session) split(ctx context.Context, rule rule, values []normaliser.Value, reply normaliser.Reply) ([]normaliser.Unit, error) {
	rows, ok := reply.(normaliser.Rows)
	if !ok {
		return nil, errors.Errorf("%s needs rows, not %T", rule.replacement, reply)
	}
	source := rule.sources[0]
	labelled, err := source.rows(rows)
	if err != nil {
		return nil, err
	}
	columns := source.definitions(rows.Columns)
	units := []normaliser.Unit{}
	err = s.runner.run(ctx, func(callbacks Callbacks) error {
		return callbacks.Split(rule.replacement, values, labelled, func(index int, values []Option[string], offered []Row) error {
			statement := rule.statements[index]
			split, err := statement.reply(offered, columns)
			if err != nil {
				return err
			}
			units = append(units, normaliser.Unit{Route: route, Key: statement.key(s.database, values), Reply: split})
			return nil
		})
	})
	return units, errors.WithStack(err)
}

// Candidate answers a statement that replaces several reference statements from their
// replies, and claims the reply of any other statement that matches a pattern by its pattern.
func (s *session) Candidate(ctx context.Context, command normaliser.Command, claim normaliser.Claim,
) (normaliser.Reply, error) { //nolint:ireturn // Replies are the contract's sum type.
	if database, selects := normaliser.SelectedDatabase(command); selects {
		s.database = database
	}
	if matched, ok := s.match(command); ok {
		for _, rule := range s.rules {
			if rule.replacement.Combines() && rule.statements[0].matches(matched.digest) {
				return s.combine(ctx, rule, matched.values, claim)
			}
			if found, named := patternOf(rule, matched.digest); named {
				return errors.WithStack2(claim(ctx, route, found.key(s.database, matched.values)))
			}
		}
	}
	return errors.WithStack2(s.fallback.Candidate(ctx, command, claim))
}

// combine answers a candidate statement with the rows a script builds from the reference
// replies it claims. A claimed reply without rows answers the statement as it is.
func (s *session) combine(ctx context.Context, rule rule, values []normaliser.Value, claim normaliser.Claim,
) (normaliser.Reply, error) { //nolint:ireturn // Replies are the contract's sum type.
	columns := []normaliser.Column{}
	claims := make([]Claim, 0, len(rule.sources))
	for _, source := range rule.sources {
		claims = append(claims, func(values []Option[string]) ([]Row, error) {
			reply, err := claim(ctx, route, source.key(s.database, values))
			if err != nil {
				return nil, newClaimError(err)
			}
			rows, ok := reply.(normaliser.Rows)
			if !ok {
				return nil, newRowlessError(reply)
			}
			columns = append(columns, rows.Columns...)
			return source.rows(rows)
		})
	}
	var rows []Row
	err := s.runner.run(ctx, func(callbacks Callbacks) error {
		var combineErr error
		rows, combineErr = callbacks.Combine(rule.replacement, values, claims)
		return errors.WithStack(combineErr)
	})
	if rowless, ok := errors.AsType[rowlessError](err); ok {
		return rowless.claimed(), nil
	}
	if failed, ok := errors.AsType[claimError](err); ok {
		return nil, failed
	}
	if err == nil {
		var built normaliser.Rows
		if built, err = rule.statements[0].reply(rows, columns); err == nil {
			return built, nil
		}
	}
	// The script failed, so the candidate statement cannot be answered.
	return nil, normaliser.UnsupportedError{Route: route, Reason: err.Error()}
}

// patternOf returns the pattern of a rule, on either side, that a statement with the digest matches.
func patternOf(rule rule, digest string) (pattern, bool) {
	for _, named := range slices.Concat(rule.statements, rule.sources) {
		if named.matches(digest) {
			return named, true
		}
	}
	return pattern{}, false
}

// matched is a single statement's pattern digest and its values.
type matched struct {
	digest string
	values []normaliser.Value
}

// match reads a command that is one SELECT statement, which may match a pattern.
func (s *session) match(command normaliser.Command) (matched, bool) {
	query, ok := command.(normaliser.Query)
	if !ok || len(query.Statements) != 1 || len(s.rules) == 0 {
		return matched{}, false
	}
	if _, selects := query.Statements[0].(*ast.SelectStmt); !selects {
		return matched{}, false
	}
	return matched{digest: digest(query.SQL), values: valuesOf(query.Statements[0])}, true
}

// rowlessError carries a claimed reply that has no rows for the script to combine.
type rowlessError struct {
	reply normaliser.Reply
}

func newRowlessError(reply normaliser.Reply) rowlessError {
	return rowlessError{reply: reply}
}

func (r rowlessError) Error() string {
	return "claimed reply has no rows"
}

// claimed is the reply that was claimed.
func (r rowlessError) claimed() normaliser.Reply { //nolint:ireturn // Replies are the contract's sum type.
	return r.reply
}

// claimError is a claim's failure, which the engine has already reported.
type claimError struct {
	err error
}

func newClaimError(err error) claimError {
	return claimError{err: err}
}

func (c claimError) Error() string {
	return c.err.Error()
}

func (c claimError) Unwrap() error {
	return c.err
}

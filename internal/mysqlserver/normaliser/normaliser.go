// Package normaliser is the contract between the MySQL proxy and what understands
// statements. The proxy owns connections, the database, pairing, saved memory and
// quarantine. A normaliser turns reference statements and their replies into units
// that egress pairs, and answers candidate statements by claiming those units.
//
// Text statements arrive parsed by TiDB's MySQL parser, and replies are plain
// values, so a normaliser never touches the wire or parses SQL itself.
package normaliser

import (
	"context"
	"fmt"
	"slices"
	"sync"

	. "github.com/alecthomas/types/optional"
	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver" // Literal values for the parser, without the rest of TiDB.

	"github.com/block/spectre/internal/model"
)

// Normaliser starts a session for each connection a service opens to the proxy.
type Normaliser interface {
	NewSession() Session
}

// Session follows one connection's commands, in the order the service sends them.
type Session interface {
	// Reference returns the units a forwarded command and its reply offer to the candidate.
	Reference(ctx context.Context, command Command, reply Reply) ([]Unit, error)
	// Candidate answers a command from claimed units. An UnsupportedError quarantines the candidate.
	Candidate(ctx context.Context, command Command, claim Claim) (Reply, error)
}

// Unit is what one candidate claim pairs with. Its route names the traffic and must
// hold no values. Its key decides the pairing, and must be JSON-like data.
type Unit struct {
	Route model.Route
	Key   any
	Reply Reply
}

// Claim waits for the reply of the reference unit with the same route and key. It
// fails, and the candidate is quarantined, when there is none.
type Claim func(ctx context.Context, route model.Route, key any) (Reply, error)

// UnsupportedError reports a candidate command that the normaliser cannot answer.
type UnsupportedError struct {
	Route  model.Route
	Reason string
}

func (e UnsupportedError) Error() string {
	return fmt.Sprintf("%s: %s", e.Route, e.Reason)
}

// Command is a statement or command that a service sent.
//
//sumtype:decl
type Command interface {
	command()
}

// Query is a text statement, and what it parsed to: several statements when the
// text holds several. A normaliser must not change them.
type Query struct {
	SQL        string
	Statements []ast.StmtNode
}

func (Query) command() {}

// UnparsedQuery is a text statement that the parser could not read.
type UnparsedQuery struct {
	SQL    string
	Reason string
}

func (UnparsedQuery) command() {}

// UseDatabase selects the default database.
type UseDatabase struct {
	Name string
}

func (UseDatabase) command() {}

// Reply is what the database answered a command with.
//
//sumtype:decl
type Reply interface {
	reply()
}

// Rows is a result set in the text protocol.
type Rows struct {
	Columns []Column
	Rows    [][]Value
}

func (Rows) reply() {}

// Value is one value of a row as text. An absent value is NULL.
type Value = Option[string]

// Column is the definition of a result column.
type Column struct {
	Schema, Table, OrgTable, Name, OrgName string
	Charset                                uint16
	Length                                 uint32
	Type                                   uint8
	Flags                                  uint16
	Decimals                               uint8
}

// OK is the reply to a command that returned no rows.
type OK struct {
	AffectedRows, InsertID uint64
	Status, Warnings       uint16
}

func (OK) reply() {}

// Error is an error that the database answered with.
type Error struct {
	Code    uint16
	State   string
	Message string
}

func (Error) reply() {}

// Parser reads text statements into commands. It is safe for concurrent use.
type Parser struct {
	// parsers are reused, because each holds buffers and none can parse concurrently.
	parsers *sync.Pool
}

// NewParser returns a parser of MySQL statements in the default SQL mode.
func NewParser() *Parser {
	return &Parser{parsers: &sync.Pool{New: func() any { return parser.New() }}}
}

// Parse returns the Query that sql parses to, or an UnparsedQuery.
func (p *Parser) Parse(sql string) Command { //nolint:ireturn // Commands are a sum type.
	sqlParser, ok := p.parsers.Get().(*parser.Parser)
	if !ok {
		sqlParser = parser.New()
	}
	defer p.parsers.Put(sqlParser)
	statements, _, err := sqlParser.Parse(sql, "", "")
	switch {
	case err != nil:
		return UnparsedQuery{SQL: sql, Reason: err.Error()}
	case len(statements) == 0:
		return UnparsedQuery{SQL: sql, Reason: "no statement"}
	}
	// The parser reuses the slice it returns for its next statement.
	return Query{SQL: sql, Statements: slices.Clone(statements)}
}

package mysql

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/protocols/mysql/normaliser"
	"github.com/block/spectre/internal/protocols/mysql/scripted"
	"github.com/block/spectre/internal/schema"
)

// Egress hashes pairings as the built-in spectre.mysql.Pairing type, so each must validate as one.
func TestPairingsAreMySQLPairings(t *testing.T) {
	binding, _ := scripted.NewBinding()
	loaded, err := schema.ParseSources(t.Context(), map[string]string{}, binding.Payloads())
	assert.NoError(t, err)
	server := &Server{schema: loaded}
	for name, by := range map[string]pairing{
		"Statement": {Key: normaliser.Key{Database: "app", Statement: "BEGIN"}},
		"Pattern": {
			Key:   normaliser.Key{Database: "app", Pattern: "SELECT ?, ?", Values: []normaliser.Value{Some("1"), None[string]()}},
			Opens: true, Transaction: Some(newTransactionID()),
		},
	} {
		t.Run(name, func(t *testing.T) {
			declared, err := loaded.Type(scripted.Pairing)
			assert.NoError(t, err)
			value, err := server.request(by)(t.Context())
			assert.NoError(t, err)
			assert.Equal(t, schema.Type(declared), value.Type())
		})
	}
}

// position is where a statement ran: in which of the connection's transactions, if any.
type position struct {
	transaction uint64
	inside      bool
}

// A connection's statements start and end transactions as MySQL does, so each statement
// is told apart from those of the connection's other transactions.
func TestTransactionsFollowTheConnectionsStatements(t *testing.T) {
	for name, test := range map[string]struct {
		statements []string
		expected   []position
	}{
		"Autocommit": {
			statements: []string{"SELECT 1", "UPDATE orders SET label = 'A' WHERE id = 7"},
			expected:   []position{{0, false}, {0, false}},
		},
		"BeginAndCommit": {
			statements: []string{"START TRANSACTION", "SELECT 1", "COMMIT", "SELECT 1", "BEGIN", "SELECT 1", "ROLLBACK"},
			expected:   []position{{1, false}, {1, true}, {1, true}, {1, false}, {2, false}, {2, true}, {2, true}},
		},
		"BeginCommitsTheOpenTransaction": {
			statements: []string{"BEGIN", "SELECT 1", "BEGIN", "SELECT 1"},
			expected:   []position{{1, false}, {1, true}, {2, false}, {2, true}},
		},
		"RollbackToSavepointStaysInside": {
			statements: []string{"BEGIN", "SAVEPOINT before", "ROLLBACK TO SAVEPOINT before", "SELECT 1", "COMMIT"},
			expected:   []position{{1, false}, {1, true}, {1, true}, {1, true}, {1, true}},
		},
		// Connector/J turns autocommit off, and every statement after a commit starts the next transaction.
		"AutocommitOff": {
			statements: []string{"SET autocommit=0", "SELECT 1", "COMMIT", "SELECT 1", "SET autocommit=1", "SELECT 1"},
			expected:   []position{{0, false}, {1, true}, {1, true}, {2, true}, {2, true}, {2, false}},
		},
		"AutocommitSpelledOtherwise": {
			statements: []string{"SET SESSION autocommit = OFF", "SELECT 1", "SET @@autocommit = ON", "SELECT 1"},
			expected:   []position{{0, false}, {1, true}, {1, true}, {1, false}},
		},
		"GlobalAndUserVariablesAreNotTheSessions": {
			statements: []string{"SET GLOBAL autocommit = 0", "SET @autocommit = 0", "SELECT 1"},
			expected:   []position{{0, false}, {0, false}, {0, false}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			parser := normaliser.NewParser()
			transactions := newTransactions()
			positions := []position{}
			for _, statement := range test.statements {
				transaction, inside, _ := transactions.next(parser.Parse(statement), None[engine.RequestID]())
				positions = append(positions, position{transaction, inside})
			}
			assert.Equal(t, test.expected, positions)
		})
	}
}

// A transaction belongs to one request, so its statements that name none, such as a COMMIT
// the driver sends itself, belong to the request its other statements name.
func TestTransactionsLendTheirRequestToStatementsThatNameNone(t *testing.T) {
	first, second := typeid.Must(typeid.New[engine.RequestID]()), typeid.Must(typeid.New[engine.RequestID]())
	for name, test := range map[string]struct {
		statements []string
		named      []Option[engine.RequestID]
		expected   []Option[engine.RequestID]
	}{
		"Autocommit": {
			statements: []string{"SELECT 1", "SELECT 1"},
			named:      []Option[engine.RequestID]{Some(first), None[engine.RequestID]()},
			expected:   []Option[engine.RequestID]{Some(first), None[engine.RequestID]()},
		},
		"CommitTakesTheTransactions": {
			statements: []string{"BEGIN", "SELECT 1", "COMMIT", "SELECT 1"},
			named:      []Option[engine.RequestID]{None[engine.RequestID](), Some(first), None[engine.RequestID](), None[engine.RequestID]()},
			expected:   []Option[engine.RequestID]{None[engine.RequestID](), Some(first), Some(first), None[engine.RequestID]()},
		},
		"BeginNamesTheTransactions": {
			statements: []string{"BEGIN", "SELECT 1", "ROLLBACK"},
			named:      []Option[engine.RequestID]{Some(first), None[engine.RequestID](), None[engine.RequestID]()},
			expected:   []Option[engine.RequestID]{Some(first), Some(first), Some(first)},
		},
		"NextTransactionStartsWithNone": {
			statements: []string{"BEGIN", "SELECT 1", "COMMIT", "BEGIN", "SELECT 1", "COMMIT"},
			named: []Option[engine.RequestID]{
				None[engine.RequestID](), Some(first), None[engine.RequestID](),
				None[engine.RequestID](), None[engine.RequestID](), None[engine.RequestID](),
			},
			expected: []Option[engine.RequestID]{
				None[engine.RequestID](), Some(first), Some(first),
				None[engine.RequestID](), None[engine.RequestID](), None[engine.RequestID](),
			},
		},
		"StatementsKeepTheirOwn": {
			statements: []string{"BEGIN", "SELECT 1", "SELECT 2", "COMMIT"},
			named:      []Option[engine.RequestID]{Some(first), Some(second), None[engine.RequestID](), None[engine.RequestID]()},
			expected:   []Option[engine.RequestID]{Some(first), Some(second), Some(second), Some(second)},
		},
		"AutocommitOff": {
			statements: []string{"SET autocommit=0", "SELECT 1", "COMMIT", "SELECT 1"},
			named:      []Option[engine.RequestID]{None[engine.RequestID](), Some(first), None[engine.RequestID](), None[engine.RequestID]()},
			expected:   []Option[engine.RequestID]{None[engine.RequestID](), Some(first), Some(first), None[engine.RequestID]()},
		},
	} {
		t.Run(name, func(t *testing.T) {
			parser := normaliser.NewParser()
			transactions := newTransactions()
			requests := []Option[engine.RequestID]{}
			for index, statement := range test.statements {
				_, _, requestID := transactions.next(parser.Parse(statement), test.named[index])
				requests = append(requests, requestID)
			}
			assert.Equal(t, test.expected, requests)
		})
	}
}

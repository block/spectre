package mysql

import (
	"fmt"
	"strings"

	. "github.com/alecthomas/types/optional"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/test_driver"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/protocols/mysql/normaliser"
)

type transactionPrefix struct{}

func (transactionPrefix) Prefix() string { return "transaction" }

// transactionID names one reference transaction, so a candidate transaction claims
// only its replies once it has claimed one.
type transactionID struct {
	typeid.TypeID[transactionPrefix]
}

func newTransactionID() transactionID {
	return typeid.Must(typeid.New[transactionID]())
}

// pairing is what egress pairs a unit by: its key, within the transaction it ran in.
type pairing struct {
	Key normaliser.Key `json:"key"`
	// Opens pairs the first unit of a transaction only with the first unit of another.
	Opens bool `json:"opens,omitzero"`
	// Transaction pairs a unit only with units of the same transaction.
	Transaction Option[transactionID] `json:"transaction,omitzero"`
}

// scopedUnit is a reference unit, what egress pairs it by, and the transaction it names
// to the candidate that claims it, if any.
type scopedUnit struct {
	normaliser.Unit
	pairing pairing
	names   Option[transactionID]
}

// transactions follows a connection's transactions through the statements it sends, as
// MySQL starts and ends them. Statements that commit implicitly, such as DDL, are not followed.
type transactions struct {
	manual bool
	open   bool
	// started counts the connection's transactions, so a new one is told from the last.
	started uint64
}

func newTransactions() *transactions {
	return &transactions{}
}

// next follows a command, and returns the transaction it ran in, if any.
func (t *transactions) next(command normaliser.Command) (transaction uint64, inside bool) {
	switch command := command.(type) {
	case normaliser.Query:
		for _, statement := range command.Statements {
			inside = t.statement(statement)
		}
	case normaliser.UnparsedQuery:
		inside = t.other()
	case normaliser.UseDatabase:
		inside = t.open
	}
	return t.started, inside
}

// statement follows one statement, and reports whether it ran in a transaction.
func (t *transactions) statement(statement ast.StmtNode) (inside bool) {
	switch statement := statement.(type) {
	case *ast.BeginStmt:
		// BEGIN commits any open transaction, and runs in neither.
		t.open = true
		t.started++
		return false
	case *ast.CommitStmt:
		inside, t.open = t.open, false
		return inside
	case *ast.RollbackStmt:
		inside = t.open
		if statement.SavepointName == "" {
			t.open = false
		}
		return inside
	case *ast.SetStmt:
		if manual, ok := autocommit(statement).Get(); ok {
			inside = t.open
			// Turning autocommit on commits the open transaction.
			t.manual, t.open = manual, t.open && manual
			return inside
		}
	}
	return t.other()
}

// other follows a statement that neither starts nor ends a transaction by itself, but
// starts one when autocommit is off.
func (t *transactions) other() (inside bool) {
	if !t.open && t.manual {
		t.open = true
		t.started++
	}
	return t.open
}

// referenceTransactions keys the units of a reference connection's transactions by the
// transaction that each statement ran in.
type referenceTransactions struct {
	transactions *transactions
	started      uint64
	current      Option[transactionID]
	// named is whether a unit has named the current transaction to the candidate.
	named bool
}

func newReferenceTransactions() *referenceTransactions {
	return &referenceTransactions{transactions: newTransactions()}
}

// follow follows a command whose units are not offered.
func (r *referenceTransactions) follow(command normaliser.Command) {
	r.transactions.next(command)
}

// scope follows a command, and keys its units by its transaction. The transaction's first
// unit pairs with any transaction's first, and names the transaction to the candidate that claims it.
func (r *referenceTransactions) scope(command normaliser.Command, units func() []normaliser.Unit) []scopedUnit {
	started, inside := r.transactions.next(command)
	offered := units()
	scoped := make([]scopedUnit, 0, len(offered))
	if !inside {
		for _, unit := range offered {
			scoped = append(scoped, scopedUnit{Unit: unit, pairing: pairing{Key: unit.Key}})
		}
		return scoped
	}
	id, ok := r.current.Get()
	if !ok || r.started != started {
		id = newTransactionID()
		r.started, r.current, r.named = started, Some(id), false
	}
	for _, unit := range offered {
		if !r.named {
			r.named = true
			scoped = append(scoped, scopedUnit{Unit: unit, pairing: pairing{Key: unit.Key, Opens: true}, names: Some(id)})
			continue
		}
		scoped = append(scoped, scopedUnit{Unit: unit, pairing: pairing{Key: unit.Key, Transaction: Some(id)}})
	}
	return scoped
}

// candidateTransactions keys a candidate connection's claims by the reference transaction
// that its current transaction pairs with, once a claimed reply names one.
type candidateTransactions struct {
	transactions *transactions
	started      uint64
	// inside is whether the current command runs in a transaction.
	inside bool
	claims Option[transactionID]
}

func newCandidateTransactions() *candidateTransactions {
	return &candidateTransactions{transactions: newTransactions()}
}

// next follows the command whose units are claimed next.
func (c *candidateTransactions) next(command normaliser.Command) {
	started, inside := c.transactions.next(command)
	if !inside || started != c.started {
		c.started, c.claims = started, None[transactionID]()
	}
	c.inside = inside
}

// key is what to claim a unit of the current command by.
func (c *candidateTransactions) key(key normaliser.Key) pairing {
	if !c.inside {
		return pairing{Key: key}
	}
	if id, bound := c.claims.Get(); bound {
		return pairing{Key: key, Transaction: Some(id)}
	}
	return pairing{Key: key, Opens: true}
}

// claimed pairs the rest of the transaction with the reference transaction a claimed reply names.
func (c *candidateTransactions) claimed(names Option[transactionID]) {
	if _, bound := c.claims.Get(); !bound && c.inside {
		c.claims = names
	}
}

// autocommit returns whether a SET statement turns the session's autocommit off, if it sets it.
func autocommit(statement *ast.SetStmt) Option[bool] {
	manual := None[bool]()
	for _, variable := range statement.Variables {
		if !variable.IsSystem || variable.IsGlobal || !strings.EqualFold(variable.Name, "autocommit") || variable.Value == nil {
			continue
		}
		var text string
		switch value := variable.Value.(type) {
		case *test_driver.ValueExpr:
			text = fmt.Sprint(value.GetValue())
		case *ast.ColumnNameExpr:
			text = value.Name.Name.O
		}
		switch strings.ToLower(text) {
		case "0", "off", "false":
			manual = Some(true)
		case "1", "on", "true":
			manual = Some(false)
		}
	}
	return manual
}

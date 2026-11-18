package mysqlserver

import (
	"sync"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/mysqlserver/normaliser"
	"github.com/block/spectre/internal/proxy"
)

// Estimated memory of a saved reply, column, row and value, besides their text.
const (
	replyBytes  = 64
	columnBytes = 128
	rowBytes    = 48
	valueBytes  = 32
)

// savedReply is a reference reply kept for a candidate claim. It holds memory from
// the budget until released.
type savedReply struct {
	reply   normaliser.Reply
	budget  *proxy.Budget
	size    int
	release sync.Once
}

// reserveReply claims a reply's memory from budget, and reports whether it fits.
func reserveReply(budget *proxy.Budget, reply normaliser.Reply) (*savedReply, bool) {
	size := replySize(reply)
	if !budget.Reserve(size) {
		return nil, false
	}
	return &savedReply{reply: reply, budget: budget, size: size}, true
}

// value is the saved reply.
func (s *savedReply) value() normaliser.Reply { //nolint:ireturn // Replies are a sum type.
	return s.reply
}

// Release returns the reply's memory to the budget.
func (s *savedReply) Release() {
	s.release.Do(func() { s.budget.Release(s.size) })
}

// replySize estimates the memory a saved reply holds.
func replySize(reply normaliser.Reply) int {
	size := replyBytes
	switch reply := reply.(type) {
	case normaliser.Rows:
		for _, column := range reply.Columns {
			size += columnBytes + len(column.Schema) + len(column.Table) + len(column.OrgTable) +
				len(column.Name) + len(column.OrgName)
		}
		for _, row := range reply.Rows {
			size += rowBytes
			for _, value := range row {
				size += valueBytes + len(value.Default(""))
			}
		}
	case normaliser.OK:
	case normaliser.Error:
		size += len(reply.State) + len(reply.Message)
	}
	return size
}

// errorReply converts a failed command. It reports false for a failure that is not
// the database's answer, such as a lost connection.
func errorReply(err error) (reply normaliser.Reply, answered bool) { //nolint:ireturn // Replies are a sum type.
	reported, ok := errors.AsType[*mysql.MyError](err)
	if !ok {
		return nil, false
	}
	return normaliser.Error{Code: reported.Code, State: reported.State, Message: reported.Message}, true
}

// resultReply converts the result of a command. It reports false for rows that
// are not in the text protocol.
func resultReply(result *mysql.Result) (reply normaliser.Reply, answered bool) { //nolint:ireturn // Replies are a sum type.
	if !result.HasResultset() {
		return normaliser.OK{AffectedRows: result.AffectedRows, InsertID: result.InsertId, Status: result.Status, Warnings: result.Warnings}, true
	}
	rows, err := rowsOf(result.Resultset)
	if err != nil {
		return nil, false
	}
	return rows, true
}

// rowsOf copies a text result set, so it outlives the buffers the client read it into.
func rowsOf(result *mysql.Resultset) (normaliser.Rows, error) {
	rows := normaliser.Rows{Columns: make([]normaliser.Column, 0, len(result.Fields)), Rows: make([][]normaliser.Value, 0, len(result.RowDatas))}
	for _, field := range result.Fields {
		rows.Columns = append(rows.Columns, normaliser.Column{
			Schema: string(field.Schema), Table: string(field.Table), OrgTable: string(field.OrgTable),
			Name: string(field.Name), OrgName: string(field.OrgName), Charset: field.Charset,
			Length: field.ColumnLength, Type: field.Type, Flags: field.Flag, Decimals: field.Decimal,
		})
	}
	for _, data := range result.RowDatas {
		row := make([]normaliser.Value, 0, len(result.Fields))
		for range result.Fields {
			value, isNull, read, err := mysql.LengthEncodedString(data)
			if err != nil {
				return normaliser.Rows{}, errors.Wrap(err, "read a text row")
			}
			data = data[read:]
			if isNull {
				row = append(row, None[string]())
				continue
			}
			row = append(row, Some(string(value)))
		}
		rows.Rows = append(rows.Rows, row)
	}
	return rows, nil
}

// resultOf converts a reply for the wire library, which sends an error as the database would.
func resultOf(reply normaliser.Reply) (*mysql.Result, error) {
	switch reply := reply.(type) {
	case normaliser.Rows:
		return mysql.NewResult(resultsetOf(reply)), nil
	case normaliser.OK:
		return &mysql.Result{AffectedRows: reply.AffectedRows, InsertId: reply.InsertID, Status: reply.Status, Warnings: reply.Warnings}, nil
	case normaliser.Error:
		return nil, &mysql.MyError{Code: reply.Code, Message: reply.Message, State: reply.State}
	}
	return nil, errors.Errorf("unknown reply %T", reply)
}

// resultsetOf encodes rows in the text protocol.
func resultsetOf(rows normaliser.Rows) *mysql.Resultset {
	result := mysql.NewResultset(len(rows.Columns))
	for index, column := range rows.Columns {
		result.Fields[index] = &mysql.Field{
			Schema: []byte(column.Schema), Table: []byte(column.Table), OrgTable: []byte(column.OrgTable),
			Name: []byte(column.Name), OrgName: []byte(column.OrgName), Charset: column.Charset,
			ColumnLength: column.Length, Type: column.Type, Flag: column.Flags, Decimal: column.Decimals,
		}
		result.FieldNames[column.Name] = index
	}
	for _, row := range rows.Rows {
		data := mysql.RowData{}
		for _, value := range row {
			text, ok := value.Get()
			if !ok {
				data = append(data, 0xfb)
				continue
			}
			data = append(data, mysql.PutLengthEncodedString([]byte(text))...)
		}
		result.RowDatas = append(result.RowDatas, data)
	}
	return result
}

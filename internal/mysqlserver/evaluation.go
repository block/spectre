package mysqlserver

import (
	"cmp"
	"slices"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"
)

// utf8mb4Charset is the collation ID of utf8mb4_0900_ai_ci, MySQL's default.
const utf8mb4Charset = 255

// evaluation is a candidate statement's rows before projection, read from the saved
// rows of its lookups. Each row is a driving row and the joined row it matched, if any.
type evaluation struct {
	selected selection
	fields   [2]map[string]*mysql.Field
	rows     [][2]row
	// joinedRead is whether any lookup of the joined table was claimed, so its columns are known.
	joinedRead bool
}

func newEvaluation(selected selection, driving map[string]*mysql.Field) *evaluation {
	return &evaluation{selected: selected, fields: [2]map[string]*mysql.Field{driving, {}}}
}

// add appends a driving row with each joined row it matched, or alone when it matched none.
func (e *evaluation) add(driving row, joined []row) {
	if len(joined) == 0 {
		e.rows = append(e.rows, [2]row{driving, {}})
		return
	}
	for _, matched := range joined {
		e.rows = append(e.rows, [2]row{driving, matched})
	}
}

// read records the columns of a claimed lookup of the joined table.
func (e *evaluation) read(joined map[string]*mysql.Field) {
	e.fields[1], e.joinedRead = joined, true
}

// field returns a column's definition. It is absent when no lookup read the column's
// table, so every value of the column is NULL.
func (e *evaluation) field(read column) (Option[*mysql.Field], error) {
	if found, ok := e.fields[side(read.joined)][read.name]; ok {
		return Some(found), nil
	}
	if read.joined && !e.joinedRead {
		return None[*mysql.Field](), nil
	}
	return None[*mysql.Field](), errors.New("statement reads a column that the reference did not")
}

// sort orders the rows as the statement asks. Only integer columns can be compared
// without the database's collations.
func (e *evaluation) sort() error {
	keys := make([]ordering, 0, len(e.selected.order))
	for _, key := range e.selected.order {
		definition, err := e.field(key.column)
		if err != nil {
			return err
		}
		field, read := definition.Get()
		if !read {
			continue
		}
		if !isInteger(field.Type) {
			return errors.New("ORDER BY is only supported on integer columns")
		}
		keys = append(keys, key)
	}
	slices.SortStableFunc(e.rows, func(left, right [2]row) int {
		for _, key := range keys {
			order := compareIntegers(cell(left, key.column), cell(right, key.column))
			if key.descending {
				order = -order
			}
			if order != 0 {
				return order
			}
		}
		return 0
	})
	return nil
}

// result encodes the statement's columns of each row as a text result.
func (e *evaluation) result() (*mysql.Result, error) {
	if err := e.sort(); err != nil {
		return nil, err
	}
	resultset := mysql.NewResultset(len(e.selected.outputs))
	for index, selectedColumn := range e.selected.outputs {
		field, err := e.outputField(selectedColumn)
		if err != nil {
			return nil, err
		}
		resultset.Fields[index] = field
		resultset.FieldNames[selectedColumn.label] = index
	}
	for _, read := range e.rows {
		data := mysql.RowData{}
		for _, selectedColumn := range e.selected.outputs {
			value, ok := cell(read, selectedColumn.column).Get()
			if !ok {
				data = append(data, 0xfb)
				continue
			}
			data = append(data, mysql.PutLengthEncodedString([]byte(value))...)
		}
		resultset.RowDatas = append(resultset.RowDatas, data)
	}
	return mysql.NewResult(resultset), nil
}

// outputField defines a result column as the reference's result did, under the
// statement's own names.
func (e *evaluation) outputField(selectedColumn output) (*mysql.Field, error) {
	read := e.selected.driving
	if joined, ok := e.selected.join.Get(); ok && selectedColumn.column.joined {
		read = joined.table
	}
	definition, err := e.field(selectedColumn.column)
	if err != nil {
		return nil, err
	}
	field := &mysql.Field{
		Schema:   []byte(read.schema),
		OrgTable: []byte(read.name),
		OrgName:  []byte(selectedColumn.column.name),
		Charset:  utf8mb4Charset,
		Type:     mysql.MYSQL_TYPE_VAR_STRING,
	}
	if saved, ok := definition.Get(); ok {
		field = cloneField(saved)
	}
	field.Table = []byte(read.alias)
	field.Name = []byte(selectedColumn.label)
	return field, nil
}

func cell(read [2]row, selected column) Option[string] {
	return read[side(selected.joined)][selected.name]
}

func isInteger(fieldType uint8) bool {
	switch fieldType {
	case mysql.MYSQL_TYPE_TINY, mysql.MYSQL_TYPE_SHORT, mysql.MYSQL_TYPE_INT24, mysql.MYSQL_TYPE_LONG,
		mysql.MYSQL_TYPE_LONGLONG, mysql.MYSQL_TYPE_YEAR:
		return true
	}
	return false
}

// compareIntegers orders decimal integers of any size, with NULL first as MySQL sorts it.
func compareIntegers(left, right Option[string]) int {
	leftText, leftOK := left.Get()
	rightText, rightOK := right.Get()
	if !leftOK || !rightOK {
		return cmp.Compare(side(leftOK), side(rightOK))
	}
	leftNegative, rightNegative := strings.HasPrefix(leftText, "-"), strings.HasPrefix(rightText, "-")
	if leftNegative != rightNegative {
		return cmp.Compare(side(rightNegative), side(leftNegative))
	}
	order := cmp.Or(cmp.Compare(len(leftText), len(rightText)), strings.Compare(leftText, rightText))
	if leftNegative {
		return -order
	}
	return order
}

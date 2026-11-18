package mysqlserver

import (
	"bytes"
	"strconv"
	"strings"
	"sync"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/proxy"
)

// Estimated memory of a saved column definition, row and value, besides the value's text.
const (
	fieldBytes = 128
	rowBytes   = 48
	cellBytes  = 32
)

// row is a table row's saved columns by lower-case name. An absent value is NULL.
type row map[string]Option[string]

// tableRows are rows of one table, with the definition of each column they hold.
type tableRows struct {
	fields map[string]*mysql.Field
	rows   []row
}

// savedRows are the rows a reference lookup read. Once reserved, they hold memory
// from the budget until released.
type savedRows struct {
	read     tableRows
	budget   Option[*proxy.Budget]
	reserved int
	release  sync.Once
}

func newSavedRows(fields map[string]*mysql.Field, rows []row) *savedRows {
	return &savedRows{read: tableRows{fields: fields, rows: rows}}
}

// table returns the rows that the reference read.
func (s *savedRows) table() tableRows {
	return s.read
}

// reserve claims the rows' memory from budget, and reports whether it fits.
func (s *savedRows) reserve(budget *proxy.Budget) (reserved bool) {
	size := len(s.read.fields) * fieldBytes
	for _, saved := range s.read.rows {
		size += rowBytes
		for _, value := range saved {
			size += cellBytes + len(value.Default(""))
		}
	}
	if !budget.Reserve(size) {
		return false
	}
	s.budget, s.reserved = Some(budget), size
	return true
}

// Release returns the rows' memory to the budget. Only reserved rows are released.
func (s *savedRows) Release() {
	budget, reserved := s.budget.Get()
	if !reserved {
		return
	}
	s.release.Do(func() { budget.Release(s.reserved) })
}

// savedLookup is a lookup with the rows the reference read for it.
type savedLookup struct {
	lookup lookup
	rows   *savedRows
}

// split divides a reference statement's result into the lookups it stands for: one of
// the driving table, and one of the joined table for each driving row.
func split(selected selection, result *mysql.Resultset, primaryKey func(read table) ([]string, error)) ([]savedLookup, error) {
	if len(result.Fields) != len(selected.outputs) {
		return nil, errors.New("result columns differ from the select list")
	}
	fields := [2]map[string]*mysql.Field{{}, {}}
	for index, selectedColumn := range selected.outputs {
		fields[side(selectedColumn.column.joined)][selectedColumn.column.name] = cloneField(result.Fields[index])
	}
	rows := make([][2]row, 0, len(result.RowDatas))
	for _, data := range result.RowDatas {
		read, err := readRow(selected.outputs, data)
		if err != nil {
			return nil, err
		}
		rows = append(rows, read)
	}
	joined, hasJoin := selected.join.Get()
	if !hasJoin {
		driving := make([]row, 0, len(rows))
		for _, read := range rows {
			driving = append(driving, read[0])
		}
		return []savedLookup{{lookup: newLookup(selected.driving, selected.where), rows: newSavedRows(fields[0], driving)}}, nil
	}
	drivingKey, err := selectedKey(selected.driving, fields[0], primaryKey)
	if err != nil {
		return nil, err
	}
	joinedKey, err := selectedKey(joined.table, fields[1], primaryKey)
	if err != nil {
		return nil, err
	}
	if _, ok := fields[0][joined.drivingColumn]; !ok {
		return nil, errors.New("a join's result must include the driving table's join column")
	}
	return splitJoin(newLookup(selected.driving, selected.where), joined, fields, rows, drivingKey, joinedKey), nil
}

// splitJoin groups a join's rows by driving row. A joined row whose key is NULL
// stands for a driving row that matched nothing.
func splitJoin(first lookup, joined join, fields [2]map[string]*mysql.Field, rows [][2]row, drivingKey, joinedKey []string) []savedLookup {
	type group struct {
		driving row
		joined  []row
	}
	groups := map[string]*group{}
	order := []*group{}
	for _, read := range rows {
		identity := identify(read[0], drivingKey)
		found, ok := groups[identity]
		if !ok {
			found = &group{driving: read[0]}
			groups[identity] = found
			order = append(order, found)
		}
		if identify(read[1], joinedKey) != "" {
			found.joined = append(found.joined, read[1])
		}
	}
	driving := make([]row, 0, len(order))
	lookups := make([]savedLookup, 1, len(order)+1)
	for _, found := range order {
		driving = append(driving, found.driving)
		// A NULL join column matches no row, so it stands for no lookup.
		if value, ok := found.driving[joined.drivingColumn].Get(); ok {
			lookups = append(lookups, savedLookup{lookup: joinedLookup(joined, value), rows: newSavedRows(fields[1], found.joined)})
		}
	}
	lookups[0] = savedLookup{lookup: first, rows: newSavedRows(fields[0], driving)}
	return lookups
}

// selectedKey returns a table's primary key, which a join's result must include to tell its rows apart.
func selectedKey(read table, fields map[string]*mysql.Field, primaryKey func(read table) ([]string, error)) ([]string, error) {
	key, err := primaryKey(read)
	if err != nil {
		return nil, err
	}
	if len(key) == 0 {
		return nil, errors.New("a joined table has no primary key")
	}
	for _, name := range key {
		if _, ok := fields[name]; !ok {
			return nil, errors.New("a join's result must include each table's primary key")
		}
	}
	return key, nil
}

// identify joins a row's key values so that different keys never collide. It is
// empty when the whole key is NULL.
func identify(read row, key []string) string {
	identity := strings.Builder{}
	for _, name := range key {
		value, ok := read[name].Get()
		if !ok {
			continue
		}
		identity.WriteString(strconv.Itoa(len(value)))
		identity.WriteByte(':')
		identity.WriteString(value)
	}
	return identity.String()
}

// readRow decodes a text result row into the driving table's columns and the joined table's.
func readRow(outputs []output, data mysql.RowData) ([2]row, error) {
	read := [2]row{{}, {}}
	position := 0
	for _, selectedColumn := range outputs {
		value, null, length, err := mysql.LengthEncodedString(data[position:])
		if err != nil {
			return read, errors.New("result row is malformed")
		}
		position += length
		cell := None[string]()
		if !null {
			cell = Some(string(value))
		}
		read[side(selectedColumn.column.joined)][selectedColumn.column.name] = cell
	}
	return read, nil
}

// side indexes the driving table's half of a row or the joined table's.
func side(joined bool) int {
	if joined {
		return 1
	}
	return 0
}

// cloneField copies a column definition out of a result, which the client library reuses.
func cloneField(field *mysql.Field) *mysql.Field {
	clone := *field
	// Without the raw packet, the definition is encoded from its fields again.
	clone.Data = nil
	clone.Schema = bytes.Clone(field.Schema)
	clone.Table = bytes.Clone(field.Table)
	clone.OrgTable = bytes.Clone(field.OrgTable)
	clone.Name = bytes.Clone(field.Name)
	clone.OrgName = bytes.Clone(field.OrgName)
	clone.DefaultValue = bytes.Clone(field.DefaultValue)
	return &clone
}

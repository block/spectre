package scripted

import (
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"
	"github.com/pingcap/tidb/pkg/parser/opcode"
	"github.com/pingcap/tidb/pkg/parser/test_driver"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

// pattern is a SELECT statement written with "?" in place of each value. Statements
// match it when they normalise alike, so their columns are the pattern's.
type pattern struct {
	text   string
	digest string
	fields []field
}

// newPattern reads a script's pattern, whose columns must all be named.
func newPattern(statements *normaliser.Parser, text string) (pattern, error) {
	query, ok := statements.Parse(text).(normaliser.Query)
	if !ok || len(query.Statements) != 1 {
		return pattern{}, errors.Errorf("pattern %q is not one statement", text)
	}
	selection, ok := query.Statements[0].(*ast.SelectStmt)
	if !ok || selection.Fields == nil {
		return pattern{}, errors.Errorf("pattern %q is not a SELECT statement", text)
	}
	tables := tablesOf(selection)
	fields := make([]field, 0, len(selection.Fields.Fields))
	for _, selected := range selection.Fields.Fields {
		if selected.WildCard != nil {
			return pattern{}, errors.Errorf("pattern %q selects *, so its columns are unknown", text)
		}
		resolved, err := newField(selected, tables)
		if err != nil {
			return pattern{}, errors.Wrapf(err, "pattern %q", text)
		}
		fields = append(fields, resolved)
	}
	return pattern{text: text, digest: digest(text), fields: fields}, nil
}

// digest is a statement without its values, comments, or spacing.
func digest(sql string) string {
	return parser.Normalize(sql, "ON")
}

// matches reports whether a statement with the digest matches the pattern.
func (p pattern) matches(statement string) bool {
	return p.digest == statement
}

// key pairs a statement of the pattern with the same values, in the same default database.
func (p pattern) key(database string, values []normaliser.Value) map[string]any {
	texts := make([]any, 0, len(values))
	for _, value := range values {
		if text, ok := value.Get(); ok {
			texts = append(texts, text)
		} else {
			texts = append(texts, nil)
		}
	}
	return map[string]any{"database": database, "pattern": p.digest, "values": texts}
}

// rows labels a reply's rows with the pattern's columns.
func (p pattern) rows(reply normaliser.Rows) ([]javascript.Row, error) {
	if len(reply.Columns) != len(p.fields) {
		return nil, errors.Errorf("reply has %d columns, but %q selects %d", len(reply.Columns), p.text, len(p.fields))
	}
	rows := make([]javascript.Row, 0, len(reply.Rows))
	for _, values := range reply.Rows {
		row := make(javascript.Row, len(values))
		for index, value := range values {
			row[p.fields[index].label()] = value
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// reply builds the reply to a statement of the pattern from labelled rows. Each column is
// defined as the source column it was selected from, if one is among sources.
func (p pattern) reply(rows []javascript.Row, sources []normaliser.Column) (normaliser.Rows, error) {
	reply := normaliser.Rows{Columns: make([]normaliser.Column, 0, len(p.fields)), Rows: make([][]normaliser.Value, 0, len(rows))}
	for _, selected := range p.fields {
		reply.Columns = append(reply.Columns, selected.column(sources))
	}
	for _, row := range rows {
		values := make([]normaliser.Value, 0, len(p.fields))
		for _, selected := range p.fields {
			value, ok := row[selected.label()]
			if !ok {
				return normaliser.Rows{}, errors.Errorf("row for %q has no %q", p.text, selected.label())
			}
			values = append(values, value)
		}
		reply.Rows = append(reply.Rows, values)
	}
	return reply, nil
}

// field is one selected column of a pattern.
type field struct {
	// labelled names the column in script rows: the AS name, or the column as written.
	labelled string
	// name and table name the column in replies.
	name, table string
	// origin is the table and column it was selected from, when it is a column.
	origin Option[origin]
	// aggregate is the function that computes it, when it is an aggregate.
	aggregate Option[aggregate]
}

func newField(selected *ast.SelectField, tables map[string]table) (field, error) {
	column, isColumn := selected.Expr.(*ast.ColumnNameExpr)
	if !isColumn {
		var text strings.Builder
		if err := selected.Expr.Restore(format.NewRestoreCtx(format.DefaultRestoreFlags, &text)); err != nil {
			return field{}, errors.Wrap(err, "restore selected expression")
		}
		name := text.String()
		if selected.AsName.O != "" {
			name = selected.AsName.O
		}
		resolved := field{labelled: name, name: name}
		if function, ok := selected.Expr.(*ast.AggregateFuncExpr); ok {
			resolved.aggregate = Some(newAggregate(function, tables))
		}
		return resolved, nil
	}
	source, alias := originOf(column, tables)
	qualifier, name := column.Name.Table.O, column.Name.Name.O
	resolved := field{labelled: name, name: name, table: alias, origin: Some(source)}
	if qualifier != "" {
		resolved.labelled = qualifier + "." + name
	}
	if selected.AsName.O != "" {
		resolved.labelled, resolved.name = selected.AsName.O, selected.AsName.O
	}
	return resolved, nil
}

// label names the column in script rows.
func (f field) label() string {
	return f.labelled
}

// column is the definition of the field's column: the source column it was selected from,
// or the type MySQL gives its aggregate. Any other column is defined as text.
func (f field) column(sources []normaliser.Column) normaliser.Column {
	if source, ok := f.origin.Get(); ok {
		if column, found := source.find(sources); found {
			column.Name, column.Table = f.name, f.table
			return column
		}
	}
	if function, ok := f.aggregate.Get(); ok {
		if column, typed := function.column(sources); typed {
			column.Name = f.name
			return column
		}
	}
	return normaliser.Column{Name: f.name, Table: f.table, Charset: utf8mb4, Type: mysql.MYSQL_TYPE_VAR_STRING}
}

// origin is a column in a table. The table is empty when the column's name does not say.
type origin struct {
	table, column string
}

// originOf resolves a column through the statement's tables, and returns the name the
// statement calls its table by, if it says.
func originOf(column *ast.ColumnNameExpr, tables map[string]table) (source origin, alias string) {
	qualifier := column.Name.Table.O
	source, alias = origin{column: column.Name.Name.O}, qualifier
	if found, ok := tables[strings.ToLower(qualifier)]; ok {
		source.table, alias = found.name, found.alias
	}
	return source, alias
}

// find returns the source column with the origin's table and column names.
func (o origin) find(sources []normaliser.Column) (normaliser.Column, bool) {
	for _, column := range sources {
		if strings.EqualFold(column.OrgName, o.column) && (o.table == "" || strings.EqualFold(column.OrgTable, o.table)) {
			return column, true
		}
	}
	return normaliser.Column{}, false
}

// utf8mb4 is the default character set of MySQL 8.
const utf8mb4 = 255

// table is a table that a statement reads, and the name the statement calls it by.
type table struct {
	name, alias string
}

// tablesOf maps the names a statement calls its tables by to the tables. With one
// table, an unqualified column is in it, so the empty name maps to it too.
func tablesOf(selection *ast.SelectStmt) map[string]table {
	tables := map[string]table{}
	if selection.From == nil {
		return tables
	}
	collector := newTableCollector(tables)
	selection.From.TableRefs.Accept(collector)
	if collector.counted() == 1 {
		for _, only := range tables {
			tables[""] = only
		}
	}
	return tables
}

// tableCollector visits a FROM clause's tables.
type tableCollector struct {
	tables map[string]table
	count  int
}

func newTableCollector(tables map[string]table) *tableCollector {
	return &tableCollector{tables: tables}
}

// counted is how many tables the collector visited.
func (c *tableCollector) counted() int {
	return c.count
}

func (c *tableCollector) Enter(node ast.Node) (ast.Node, bool) {
	source, ok := node.(*ast.TableSource)
	if !ok {
		return node, false
	}
	name, ok := source.Source.(*ast.TableName)
	if !ok {
		return node, false
	}
	alias := name.Name.O
	if source.AsName.O != "" {
		alias = source.AsName.O
	}
	c.tables[strings.ToLower(alias)] = table{name: name.Name.O, alias: alias}
	c.count++
	return node, true
}

func (c *tableCollector) Leave(node ast.Node) (ast.Node, bool) {
	return node, true
}

// valuesOf returns a statement's literal values in the order they are written, as text.
func valuesOf(statement ast.StmtNode) []normaliser.Value {
	collector := newValueCollector()
	statement.Accept(collector)
	return collector.collected()
}

// valueCollector visits a statement's literal values.
type valueCollector struct {
	values []normaliser.Value
}

func newValueCollector() *valueCollector {
	return &valueCollector{}
}

// collected is the values the collector visited, in order.
func (c *valueCollector) collected() []normaliser.Value {
	return c.values
}

func (c *valueCollector) Enter(node ast.Node) (ast.Node, bool) {
	if negated, ok := node.(*ast.UnaryOperationExpr); ok && negated.Op == opcode.Minus {
		if value, literal := negated.V.(*test_driver.ValueExpr); literal {
			text, _ := textOf(value).Get()
			c.values = append(c.values, Some("-"+text))
			return node, true
		}
	}
	if value, ok := node.(*test_driver.ValueExpr); ok {
		c.values = append(c.values, textOf(value))
		return node, true
	}
	return node, false
}

func (c *valueCollector) Leave(node ast.Node) (ast.Node, bool) {
	return node, true
}

// textOf is a literal value as MySQL's text protocol would send it.
func textOf(value *test_driver.ValueExpr) normaliser.Value {
	switch value.Kind() {
	case test_driver.KindNull:
		return None[string]()
	case test_driver.KindString, test_driver.KindBytes:
		return Some(value.GetString())
	}
	var text strings.Builder
	value.Format(&text)
	return Some(text.String())
}

package mysqlserver

import (
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/opcode"
	// The parser needs a driver for literal values, and this is its standalone one.
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// table is a table that a statement reads. Its columns are qualified with alias.
type table struct {
	schema string
	name   string
	alias  string
}

// column is a column of a statement's driving table, or of its joined table.
type column struct {
	joined bool
	name   string
}

// output is a column a statement returns, under label.
type output struct {
	column column
	label  string
}

// ordering is one key a statement sorts its rows by.
type ordering struct {
	column     column
	descending bool
}

// join is a left join of table, on its column equalling drivingColumn.
type join struct {
	table         table
	column        string
	drivingColumn string
}

// selection is a SELECT reduced to lookups: equalities on a driving table, and at
// most one left join that reads the joined table once per driving row.
type selection struct {
	driving table
	where   map[string]string
	join    Option[join]
	outputs []output
	order   []ordering
}

// parseSelection reads the only statement of query, whose unqualified tables are in
// database. Errors never quote the query, because quarantine reasons leave the mirror.
func parseSelection(sqlParser *parser.Parser, query string, database string) (selection, error) {
	statements, _, err := sqlParser.Parse(query, "", "")
	if err != nil {
		return selection{}, errors.New("statement cannot be parsed")
	}
	if len(statements) != 1 {
		return selection{}, errors.New("only single statements are supported")
	}
	statement, ok := statements[0].(*ast.SelectStmt)
	if !ok {
		return selection{}, errors.New("only SELECT is supported")
	}
	if err := checkClauses(statement); err != nil {
		return selection{}, err
	}
	parsed := selection{where: map[string]string{}}
	if err := readTables(&parsed, statement.From.TableRefs, database); err != nil {
		return selection{}, err
	}
	if statement.Where != nil {
		if err := readWhere(&parsed, statement.Where); err != nil {
			return selection{}, err
		}
	}
	if err := readOutputs(&parsed, statement.Fields.Fields); err != nil {
		return selection{}, err
	}
	if statement.OrderBy != nil {
		if err := readOrder(&parsed, statement.OrderBy.Items); err != nil {
			return selection{}, err
		}
	}
	return parsed, nil
}

// joinedLookup returns the read of the joined table for a driving row whose join column is value.
func joinedLookup(joined join, value string) lookup {
	return newLookup(joined.table, map[string]string{joined.column: value})
}

func readTables(s *selection, tables *ast.Join, database string) error {
	if tables.Using != nil || tables.NaturalJoin || tables.StraightJoin {
		return errors.New("only joins with ON are supported")
	}
	driving, err := readTable(tables.Left, database)
	if err != nil {
		return err
	}
	s.driving = driving
	if tables.Right == nil {
		return nil
	}
	if tables.Tp != ast.LeftJoin || tables.On == nil {
		return errors.New("only LEFT JOIN with ON is supported")
	}
	joined, err := readTable(tables.Right, database)
	if err != nil {
		return err
	}
	if joined.alias == driving.alias {
		return errors.New("joined tables need distinct names")
	}
	condition, ok := tables.On.Expr.(*ast.BinaryOperationExpr)
	if !ok || condition.Op != opcode.EQ {
		return errors.New("only joins on one equality are supported")
	}
	left, leftOK := condition.L.(*ast.ColumnNameExpr)
	right, rightOK := condition.R.(*ast.ColumnNameExpr)
	if !leftOK || !rightOK {
		return errors.New("only joins on two columns are supported")
	}
	s.join = Some(join{table: joined})
	first, err := resolve(s.driving, s.join, left.Name)
	if err != nil {
		return err
	}
	second, err := resolve(s.driving, s.join, right.Name)
	if err != nil {
		return err
	}
	if first.joined == second.joined {
		return errors.New("a join must compare a column of each table")
	}
	if first.joined {
		first, second = second, first
	}
	s.join = Some(join{table: joined, column: second.name, drivingColumn: first.name})
	return nil
}

func readWhere(s *selection, where ast.ExprNode) error {
	if parenthesised, ok := where.(*ast.ParenthesesExpr); ok {
		return readWhere(s, parenthesised.Expr)
	}
	condition, ok := where.(*ast.BinaryOperationExpr)
	if !ok {
		return errors.New("only equalities joined by AND are supported in WHERE")
	}
	if condition.Op == opcode.LogicAnd {
		if err := readWhere(s, condition.L); err != nil {
			return err
		}
		return readWhere(s, condition.R)
	}
	if condition.Op != opcode.EQ {
		return errors.New("only equalities joined by AND are supported in WHERE")
	}
	name, value := condition.L, condition.R
	if _, literal := name.(ast.ValueExpr); literal {
		name, value = value, name
	}
	named, ok := name.(*ast.ColumnNameExpr)
	if !ok {
		return errors.New("WHERE must compare a column with a literal")
	}
	compared, err := resolve(s.driving, s.join, named.Name)
	if err != nil {
		return err
	}
	if compared.joined {
		return errors.New("WHERE on the joined table is not supported")
	}
	text, err := literalText(value)
	if err != nil {
		return err
	}
	if _, repeated := s.where[compared.name]; repeated {
		return errors.New("WHERE compares a column more than once")
	}
	s.where[compared.name] = text
	return nil
}

func readOutputs(s *selection, fields []*ast.SelectField) error {
	for _, field := range fields {
		if field.WildCard != nil {
			return errors.New("SELECT * is not supported")
		}
		named, ok := field.Expr.(*ast.ColumnNameExpr)
		if !ok {
			return errors.New("only columns are supported in the select list")
		}
		selected, err := resolve(s.driving, s.join, named.Name)
		if err != nil {
			return err
		}
		label := field.AsName.O
		if label == "" {
			label = named.Name.Name.O
		}
		s.outputs = append(s.outputs, output{column: selected, label: label})
	}
	return nil
}

func readOrder(s *selection, items []*ast.ByItem) error {
	for _, item := range items {
		named, ok := item.Expr.(*ast.ColumnNameExpr)
		if !ok {
			return errors.New("only columns are supported in ORDER BY")
		}
		sorted, err := resolve(s.driving, s.join, named.Name)
		if err != nil {
			return err
		}
		s.order = append(s.order, ordering{column: sorted, descending: item.Desc})
	}
	return nil
}

// resolve finds the table of a column name. Columns of a join must be qualified.
func resolve(driving table, joinedTable Option[join], name *ast.ColumnName) (column, error) {
	lower := strings.ToLower(name.Name.O)
	joined, hasJoin := joinedTable.Get()
	switch qualifier := name.Table.O; {
	case qualifier == "" && !hasJoin, qualifier == driving.alias:
		return column{name: lower}, nil
	case hasJoin && qualifier == joined.table.alias:
		return column{joined: true, name: lower}, nil
	case qualifier == "":
		return column{}, errors.New("columns of a join must name their table")
	}
	return column{}, errors.New("a column names a table the statement does not read")
}

// checkClauses refuses every clause that lookups cannot express.
func checkClauses(statement *ast.SelectStmt) error {
	switch {
	case statement.Kind != ast.SelectStmtKindSelect, statement.From == nil, statement.Fields == nil:
		return errors.New("only SELECT from a table is supported")
	case statement.Distinct, statement.GroupBy != nil, statement.Having != nil, statement.WindowSpecs != nil:
		return errors.New("DISTINCT, GROUP BY, HAVING and windows are not supported")
	case statement.Limit != nil:
		return errors.New("LIMIT is not supported")
	case statement.LockInfo != nil, statement.SelectIntoOpt != nil, statement.With != nil:
		return errors.New("locking reads, SELECT INTO and WITH are not supported")
	}
	return nil
}

func readTable(node ast.ResultSetNode, database string) (table, error) {
	source, ok := node.(*ast.TableSource)
	if !ok {
		return table{}, errors.New("only one join is supported")
	}
	named, ok := source.Source.(*ast.TableName)
	if !ok {
		return table{}, errors.New("subqueries are not supported")
	}
	if len(named.PartitionNames) > 0 {
		return table{}, errors.New("partition selection is not supported")
	}
	read := table{schema: named.Schema.O, name: named.Name.O, alias: source.AsName.O}
	if read.schema == "" {
		read.schema = database
	}
	if read.schema == "" {
		return table{}, errors.New("no database is selected")
	}
	if read.alias == "" {
		read.alias = read.name
	}
	return read, nil
}

// literalText returns a literal as MySQL sends it in a text result, so a value
// from a statement equals the same value read from a row.
func literalText(node ast.ExprNode) (string, error) {
	literal, ok := node.(ast.ValueExpr)
	if !ok {
		return "", errors.New("WHERE must compare a column with a literal")
	}
	switch value := literal.GetValue().(type) {
	case int64:
		return strconv.FormatInt(value, 10), nil
	case uint64:
		return strconv.FormatUint(value, 10), nil
	case string:
		return value, nil
	}
	return "", errors.New("only integer and string literals are supported")
}

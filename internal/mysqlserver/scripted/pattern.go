package scripted

import (
	"context"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
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
	// labels name the selected columns in script rows, in order.
	labels []string
}

// newPattern reads a script's pattern, whose columns must all be named, each differently,
// and typed without its values.
func newPattern(statements *normaliser.Parser, text string) (pattern, error) {
	query, ok := statements.Parse(text).(normaliser.Query)
	if !ok || len(query.Statements) != 1 {
		return pattern{}, errors.Errorf("pattern %q is not one statement", text)
	}
	selection, ok := query.Statements[0].(*ast.SelectStmt)
	if !ok || selection.Fields == nil {
		return pattern{}, errors.Errorf("pattern %q is not a SELECT statement", text)
	}
	labels := make([]string, 0, len(selection.Fields.Fields))
	for _, selected := range selection.Fields.Fields {
		if selected.WildCard != nil {
			return pattern{}, errors.Errorf("pattern %q selects *, so its columns are unknown", text)
		}
		if selectsValue(selected.Expr) {
			return pattern{}, errors.Errorf("pattern %q selects a ?, whose type MySQL only knows once it runs", text)
		}
		label, err := labelOf(selected)
		if err != nil {
			return pattern{}, errors.Wrapf(err, "pattern %q", text)
		}
		for _, other := range labels {
			if other == label {
				return pattern{}, errors.Errorf("pattern %q selects two columns labelled %q, so script rows cannot hold both", text, label)
			}
		}
		labels = append(labels, label)
	}
	return pattern{text: text, digest: digest(text), labels: labels}, nil
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

// columns returns the columns that the database describes for the pattern in a default database.
func (p pattern) columns(ctx context.Context, database string, described *columns) ([]normaliser.Column, error) {
	return described.of(ctx, database, p.digest, p.text)
}

// rows labels a reply's rows with the pattern's columns.
func (p pattern) rows(reply normaliser.Rows) ([]javascript.Row, error) {
	if len(reply.Columns) != len(p.labels) {
		return nil, errors.Errorf("reply has %d columns, but %q selects %d", len(reply.Columns), p.text, len(p.labels))
	}
	rows := make([]javascript.Row, 0, len(reply.Rows))
	for _, values := range reply.Rows {
		row := make(javascript.Row, len(values))
		for index, value := range values {
			row[p.labels[index]] = value
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// reply builds the reply to a statement of the pattern from labelled rows, with the
// columns the database describes for it.
func (p pattern) reply(rows []javascript.Row, columns []normaliser.Column) (normaliser.Rows, error) {
	if len(columns) != len(p.labels) {
		return normaliser.Rows{}, errors.Errorf("the database describes %d columns, but %q selects %d", len(columns), p.text, len(p.labels))
	}
	reply := normaliser.Rows{Columns: columns, Rows: make([][]normaliser.Value, 0, len(rows))}
	for _, row := range rows {
		values := make([]normaliser.Value, 0, len(p.labels))
		for _, label := range p.labels {
			value, ok := row[label]
			if !ok {
				return normaliser.Rows{}, errors.Errorf("row for %q has no %q", p.text, label)
			}
			values = append(values, value)
		}
		reply.Rows = append(reply.Rows, values)
	}
	return reply, nil
}

// labelOf names a selected column in script rows: its AS name, or the column or
// expression as the pattern writes it.
func labelOf(selected *ast.SelectField) (string, error) {
	if selected.AsName.O != "" {
		return selected.AsName.O, nil
	}
	if column, ok := selected.Expr.(*ast.ColumnNameExpr); ok {
		if qualifier := column.Name.Table.O; qualifier != "" {
			return qualifier + "." + column.Name.Name.O, nil
		}
		return column.Name.Name.O, nil
	}
	var text strings.Builder
	if err := selected.Expr.Restore(format.NewRestoreCtx(format.DefaultRestoreFlags, &text)); err != nil {
		return "", errors.Wrap(err, "restore selected expression")
	}
	return text.String(), nil
}

// selectsValue reports whether a selected expression is or holds a "?".
func selectsValue(expression ast.ExprNode) bool {
	finder := newParameterFinder()
	expression.Accept(finder)
	return finder.found()
}

// parameterFinder visits an expression for "?" placeholders.
type parameterFinder struct {
	parameter bool
}

func newParameterFinder() *parameterFinder {
	return &parameterFinder{}
}

// found reports whether the finder visited a placeholder.
func (f *parameterFinder) found() bool {
	return f.parameter
}

func (f *parameterFinder) Enter(node ast.Node) (ast.Node, bool) {
	if _, ok := node.(*test_driver.ParamMarkerExpr); ok {
		f.parameter = true
	}
	return node, f.parameter
}

func (f *parameterFinder) Leave(node ast.Node) (ast.Node, bool) {
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

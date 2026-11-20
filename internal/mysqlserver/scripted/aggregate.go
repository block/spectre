package scripted

import (
	"strings"

	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/pingcap/tidb/pkg/parser/ast"

	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

const (
	// binary is the character set of columns that hold no text.
	binary = 63
	// countLength is the display length MySQL gives a COUNT.
	countLength = 21
	// sumDigits and averageDigits are what SUM and AVG add to their argument's length.
	sumDigits     = 22
	averageDigits = 5
	// averageDecimals is what AVG adds to its argument's decimals.
	averageDecimals = 4
	// doubleLength and doubleDecimals are a computed DOUBLE's length and decimals.
	doubleLength   = 23
	doubleDecimals = 31
	// multipleKeyFlag marks a column in a non-unique index.
	multipleKeyFlag = 8
)

// aggregate is an aggregate function of a selected column, typed as MySQL types its result.
type aggregate struct {
	function string
	argument Option[origin]
}

func newAggregate(function *ast.AggregateFuncExpr, tables map[string]table) aggregate {
	computed := aggregate{function: strings.ToLower(function.F)}
	if len(function.Args) == 1 {
		if column, ok := function.Args[0].(*ast.ColumnNameExpr); ok {
			source, _ := originOf(column, tables)
			computed.argument = Some(source)
		}
	}
	return computed
}

// column is the definition MySQL gives the result, from the argument's source column.
// It is false for a function or argument it cannot type.
func (a aggregate) column(sources []normaliser.Column) (normaliser.Column, bool) {
	if a.function == ast.AggFuncCount {
		return normaliser.Column{Charset: binary, Length: countLength, Type: mysql.MYSQL_TYPE_LONGLONG, Flags: mysql.NOT_NULL_FLAG}, true
	}
	argument, ok := a.argument.Get()
	if !ok {
		return normaliser.Column{}, false
	}
	source, found := argument.find(sources)
	if !found {
		return normaliser.Column{}, false
	}
	switch a.function {
	case ast.AggFuncMin, ast.AggFuncMax:
		// An empty group has no minimum, and the result is no longer a key.
		keys := uint16(mysql.NOT_NULL_FLAG | mysql.PRI_KEY_FLAG | mysql.UNIQUE_KEY_FLAG | multipleKeyFlag | mysql.PART_KEY_FLAG | mysql.AUTO_INCREMENT_FLAG)
		return normaliser.Column{Charset: source.Charset, Length: source.Length, Type: source.Type, Flags: source.Flags &^ keys, Decimals: source.Decimals}, true
	case ast.AggFuncSum, ast.AggFuncAvg:
		if source.Type == mysql.MYSQL_TYPE_FLOAT || source.Type == mysql.MYSQL_TYPE_DOUBLE {
			return normaliser.Column{Charset: binary, Length: doubleLength, Type: mysql.MYSQL_TYPE_DOUBLE, Decimals: doubleDecimals}, true
		}
		if !numeric(source.Type) {
			return normaliser.Column{}, false
		}
		if a.function == ast.AggFuncSum {
			return normaliser.Column{Charset: binary, Length: source.Length + sumDigits, Type: mysql.MYSQL_TYPE_NEWDECIMAL, Decimals: source.Decimals}, true
		}
		return normaliser.Column{
			Charset: binary, Length: source.Length + averageDigits, Type: mysql.MYSQL_TYPE_NEWDECIMAL,
			Flags: mysql.BINARY_FLAG, Decimals: source.Decimals + averageDecimals,
		}, true
	}
	return normaliser.Column{}, false
}

// numeric reports whether SUM and AVG compute a DECIMAL from a column of type kind.
func numeric(kind uint8) bool {
	switch kind {
	case mysql.MYSQL_TYPE_TINY, mysql.MYSQL_TYPE_SHORT, mysql.MYSQL_TYPE_INT24, mysql.MYSQL_TYPE_LONG,
		mysql.MYSQL_TYPE_LONGLONG, mysql.MYSQL_TYPE_DECIMAL, mysql.MYSQL_TYPE_NEWDECIMAL:
		return true
	}
	return false
}

package mysqlserver

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/pingcap/tidb/pkg/parser"

	"github.com/block/spectre/internal/model"
)

const (
	selectOrders      = "SELECT id, label FROM orders WHERE merchant_id = 1 ORDER BY id"
	selectOrdersItems = "SELECT o.id, o.label, i.id AS item, i.sku FROM orders o" +
		" LEFT JOIN items i ON i.order_id = o.id WHERE o.merchant_id = 1 ORDER BY o.id, i.id DESC"
)

func TestParseSelection(t *testing.T) {
	orders := table{schema: "shop", name: "orders", alias: "orders"}
	for _, test := range []struct {
		name     string
		query    string
		expected selection
	}{
		{
			name:  "OneTable",
			query: selectOrders,
			expected: selection{
				driving: orders,
				where:   map[string]string{"merchant_id": "1"},
				outputs: []output{{column: column{name: "id"}, label: "id"}, {column: column{name: "label"}, label: "label"}},
				order:   []ordering{{column: column{name: "id"}}},
			},
		},
		{
			name:  "SeveralEqualities",
			query: "SELECT ID FROM shop.orders WHERE (merchant_id = 1 AND 'A' = label)",
			expected: selection{
				driving: orders,
				where:   map[string]string{"merchant_id": "1", "label": "A"},
				outputs: []output{{column: column{name: "id"}, label: "ID"}},
			},
		},
		{
			name:  "LeftJoin",
			query: selectOrdersItems,
			expected: selection{
				driving: table{schema: "shop", name: "orders", alias: "o"},
				where:   map[string]string{"merchant_id": "1"},
				join: Some(join{
					table:         table{schema: "shop", name: "items", alias: "i"},
					column:        "order_id",
					drivingColumn: "id",
				}),
				outputs: []output{
					{column: column{name: "id"}, label: "id"},
					{column: column{name: "label"}, label: "label"},
					{column: column{joined: true, name: "id"}, label: "item"},
					{column: column{joined: true, name: "sku"}, label: "sku"},
				},
				order: []ordering{{column: column{name: "id"}}, {column: column{joined: true, name: "id"}, descending: true}},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, err := parseSelection(parser.New(), test.query, "shop")
			assert.NoError(t, err)
			assert.Equal(t, test.expected, selected)
		})
	}
}

func TestParseSelectionRefusesWhatLookupsCannotExpress(t *testing.T) {
	for _, test := range []struct {
		name     string
		query    string
		database string
		expected string
	}{
		{name: "Malformed", query: "SELEC id", database: "shop", expected: "statement cannot be parsed"},
		{name: "Write", query: "UPDATE orders SET label = 'x'", database: "shop", expected: "only SELECT is supported"},
		{name: "Wildcard", query: "SELECT * FROM orders", database: "shop", expected: "SELECT * is not supported"},
		{name: "Range", query: "SELECT id FROM orders WHERE merchant_id > 1", database: "shop", expected: "only equalities joined by AND are supported in WHERE"},
		{name: "Limit", query: "SELECT id FROM orders LIMIT 1", database: "shop", expected: "LIMIT is not supported"},
		{name: "InnerJoin", query: "SELECT o.id FROM orders o JOIN items i ON i.order_id = o.id", database: "shop", expected: "only LEFT JOIN with ON is supported"},
		{name: "UnqualifiedJoinColumn", query: "SELECT id FROM orders o LEFT JOIN items i ON i.order_id = o.id", database: "shop", expected: "columns of a join must name their table"},
		{name: "JoinedTableInWhere", query: "SELECT o.id FROM orders o LEFT JOIN items i ON i.order_id = o.id WHERE i.sku = 'x'", database: "shop", expected: "WHERE on the joined table is not supported"},
		{name: "NoDatabase", query: "SELECT id FROM orders", expected: "no database is selected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseSelection(parser.New(), test.query, test.database)
			assert.EqualError(t, err, test.expected)
		})
	}
}

func TestJoinSplitsIntoTheLookupsOfSeparateSelects(t *testing.T) {
	// Order 7 has two items and order 8 has none, so its joined columns are NULL.
	joined := lookupsOf(t, selectOrdersItems,
		[]Option[string]{Some("7"), Some("A"), Some("2"), None[string]()},
		[]Option[string]{Some("7"), Some("A"), Some("1"), Some("apple")},
		[]Option[string]{Some("8"), Some("B"), None[string](), None[string]()},
	)
	separate := lookupsOf(t, selectOrders,
		[]Option[string]{Some("7"), Some("A")},
		[]Option[string]{Some("8"), Some("B")},
	)
	separate = append(separate, lookupsOf(t, "SELECT id, sku FROM items WHERE order_id = 7 ORDER BY id DESC",
		[]Option[string]{Some("2"), None[string]()},
		[]Option[string]{Some("1"), Some("apple")},
	)...)
	separate = append(separate, lookupsOf(t, "SELECT id, sku FROM items WHERE order_id = 8 ORDER BY id DESC")...)

	assert.Equal(t, separate, joined)
}

// readLookup is a lookup as egress hashes it, with the rows saved for it.
type readLookup struct {
	route model.Route
	where map[string]string
	rows  []row
}

// lookupsOf splits a text result of query, whose tables have the primary key id.
func lookupsOf(t *testing.T, query string, rows ...[]Option[string]) []readLookup {
	t.Helper()
	selected, err := parseSelection(parser.New(), query, "shop")
	assert.NoError(t, err)
	result := mysql.NewResultset(len(selected.outputs))
	for index, selectedColumn := range selected.outputs {
		result.Fields[index] = &mysql.Field{Name: []byte(selectedColumn.label), Type: mysql.MYSQL_TYPE_VAR_STRING}
	}
	for _, values := range rows {
		data := mysql.RowData{}
		for _, value := range values {
			text, ok := value.Get()
			if !ok {
				data = append(data, 0xfb)
				continue
			}
			data = append(data, mysql.PutLengthEncodedString([]byte(text))...)
		}
		result.RowDatas = append(result.RowDatas, data)
	}
	saved, err := split(selected, result, func(table) ([]string, error) { return []string{"id"}, nil })
	assert.NoError(t, err)
	lookups := make([]readLookup, 0, len(saved))
	for _, one := range saved {
		lookups = append(lookups, readLookup{route: one.lookup.route(), where: one.lookup.where, rows: one.rows.table().rows})
	}
	return lookups
}

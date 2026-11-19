//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/integration/it"
	"github.com/block/spectre/internal/model"
)

const (
	// Merchant 1 has three orders, and order 8 has no items.
	merchantOneOrders = `{"orders":[` +
		`{"id":7,"label":"A","items":[{"id":1,"sku":"apple","quantity":2},{"id":2,"quantity":1}]},` +
		`{"id":8,"label":"B"},` +
		`{"id":9,"label":"C","items":[{"id":3,"sku":"pear","quantity":5}]}]}`

	ordersSchema = `
		declare module "orders" {
		  export interface MerchantOrders {
		    orders?: Order[];
		  }

		  export interface Order {
		    id: number;
		    label: string;
		    items?: Item[];
		  }

		  export interface Item {
		    id: number;
		    sku?: string;
		    quantity: number;
		  }
		}
	`

	selectOrdersDigest = "SELECT `id` , `label` FROM `orders` WHERE `merchant_id` = ? ORDER BY `id`"
	selectItemsDigest  = "SELECT `id` , `sku` , `quantity` FROM `items` WHERE `order_id` = ? ORDER BY `id`"
)

func TestMySQLReferenceReadsTheDatabase(t *testing.T) {
	m := startOrdersMirror(t)

	m.Ingress.Get("/merchants/1/orders").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneOrders)
	// Only the reference's statements reach the database, as the server digests them.
	m.MySQL().ExpectStatements(map[string]uint64{selectOrdersDigest: 1, selectItemsDigest: 3})
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantineUnable, "command", "statements are not supported"))
}

func TestMySQLCandidateStatementQuarantines(t *testing.T) {
	m := startOrdersMirror(t)

	// Connecting selects the database, which the candidate may do.
	assert.NoError(t, m.MySQL().Candidate.PingContext(t.Context()))
	_, err := selectItemsOf(t.Context(), m.MySQL().Candidate, 7)
	assert.Contains(t, err.Error(), "SPECTRE: statements are not supported")
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantineUnable, "command", "statements are not supported"))
	m.MySQL().ExpectStatements(map[string]uint64{})
}

func TestMySQLReferenceGetsTheDatabaseError(t *testing.T) {
	m := startOrdersMirror(t)

	_, err := m.MySQL().Reference.ExecContext(t.Context(), "SELECT id FROM missing")
	assert.Contains(t, err.Error(), "Error 1146 (42S02): Table '"+m.MySQL().Name+".missing' doesn't exist")
}

func TestMySQLReferenceConnectsWithTheProxyCredentials(t *testing.T) {
	m := startOrdersMirror(t)

	// The proxy accepts credentials the database does not know, which never reach it.
	assert.NoError(t, m.MySQL().OpenReference().PingContext(t.Context()))
}

func TestMySQLReferenceGetsTheDatabaseRefusal(t *testing.T) {
	m := startOrdersMirror(t)

	m.MySQL().SetProxyPassword("wrong")
	err := m.MySQL().OpenReference().PingContext(t.Context())
	assert.Contains(t, err.Error(), "Error 1045 (28000): Access denied for user 'it_")
}

func TestMySQLProxyReadsTheRotatedPassword(t *testing.T) {
	m := startOrdersMirror(t)

	m.MySQL().RotatePassword("rotated")
	err := m.MySQL().OpenReference().PingContext(t.Context())
	assert.Contains(t, err.Error(), "Error 1045 (28000): Access denied for user 'it_")
	m.MySQL().SetProxyPassword("rotated")
	assert.NoError(t, m.MySQL().OpenReference().PingContext(t.Context()))
}

func TestMySQLReferencePreparedStatementsReachTheDatabase(t *testing.T) {
	m := startOrdersMirror(t)

	prepared, err := m.MySQL().Reference.PrepareContext(t.Context(), "SELECT sku FROM items WHERE id = ?")
	assert.NoError(t, err)
	defer prepared.Close()
	var sku string
	assert.NoError(t, prepared.QueryRowContext(t.Context(), 3).Scan(&sku))
	assert.Equal(t, "pear", sku)
}

func TestMySQLCandidatePreparedStatementQuarantines(t *testing.T) {
	m := startOrdersMirror(t)

	_, err := m.MySQL().Candidate.PrepareContext(t.Context(), "SELECT sku FROM items WHERE id = ?") //nolint:sqlclosecheck // Preparing fails.
	assert.Contains(t, err.Error(), "SPECTRE: prepared statements are not supported")
	m.ExpectQuarantine(it.MySQLQuarantine(model.QuarantineUnable, "command", "prepared statements are not supported"))
}

// startOrdersMirror mirrors two copies of an orders service that reads one database.
func startOrdersMirror(t *testing.T) *it.Mirror {
	t.Helper()
	return it.Start(t,
		it.Schema(ordersSchema),
		it.Scripts(`
			import { ingress } from "spectre";
			import type { MerchantOrders } from "orders";

			ingress.http("GET /merchants/{merchant}/orders").match<MerchantOrders>();
		`),
		it.MySQL(
			"CREATE TABLE orders (id BIGINT PRIMARY KEY, merchant_id BIGINT NOT NULL, label VARCHAR(32) NOT NULL)",
			"CREATE TABLE items (id BIGINT PRIMARY KEY, order_id BIGINT NOT NULL, sku VARCHAR(32), quantity INT NOT NULL)",
			"INSERT INTO orders VALUES (7, 1, 'A'), (8, 1, 'B'), (9, 1, 'C'), (10, 2, 'D')",
			"INSERT INTO items VALUES (1, 7, 'apple', 2), (2, 7, NULL, 1), (3, 9, 'pear', 5), (4, 10, 'plum', 1)",
		),
		it.Reference(ordersService()),
		it.Candidate(ordersService()),
	)
}

// order is a merchant's order with its items, as the orders service returns it.
type order struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
	Items []item `json:"items,omitempty"`
}

type item struct {
	ID       int64          `json:"id"`
	SKU      Option[string] `json:"sku,omitzero"`
	Quantity int64          `json:"quantity"`
}

// ordersService serves a merchant's orders, read from the mirror's database with a
// statement for the orders and one for the items of each order.
func ordersService() it.Service {
	return func(t *testing.T, backend it.Backend) {
		t.Helper()
		database := backend.MySQL.MustGet()
		backend.HTTP.HandleFunc("GET /merchants/{merchant}/orders", func(writer http.ResponseWriter, request *http.Request) {
			merchant, err := strconv.ParseInt(request.PathValue("merchant"), 10, 64)
			if err != nil {
				http.Error(writer, "merchant must be a number", http.StatusBadRequest)
				return
			}
			orders, err := selectOrders(request.Context(), database, merchant)
			if err != nil {
				http.Error(writer, "orders cannot be read", http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string][]order{"orders": orders})
		})
	}
}

// selectOrders selects the orders, then the items of each order with a statement of its own.
func selectOrders(ctx context.Context, database *sql.DB, merchant int64) ([]order, error) {
	rows, err := database.QueryContext(ctx, "SELECT id, label FROM orders WHERE merchant_id = ? ORDER BY id", merchant)
	if err != nil {
		return nil, errors.Wrap(err, "select orders")
	}
	defer rows.Close()
	orders := []order{}
	for rows.Next() {
		read := order{}
		if err := rows.Scan(&read.ID, &read.Label); err != nil {
			return nil, errors.Wrap(err, "read order")
		}
		orders = append(orders, read)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "read orders")
	}
	for index := range orders {
		items, err := selectItemsOf(ctx, database, orders[index].ID)
		if err != nil {
			return nil, err
		}
		orders[index].Items = items
	}
	return orders, nil
}

func selectItemsOf(ctx context.Context, database *sql.DB, orderID int64) ([]item, error) {
	rows, err := database.QueryContext(ctx, "SELECT id, sku, quantity FROM items WHERE order_id = ? ORDER BY id", orderID)
	if err != nil {
		return nil, errors.Wrap(err, "select items")
	}
	defer rows.Close()
	items := []item{}
	for rows.Next() {
		read, sku := item{}, sql.NullString{}
		if err := rows.Scan(&read.ID, &sku, &read.Quantity); err != nil {
			return nil, errors.Wrap(err, "read item")
		}
		if sku.Valid {
			read.SKU = Some(sku.String)
		}
		items = append(items, read)
	}
	return items, errors.Wrap(rows.Err(), "read items")
}

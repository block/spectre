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
)

const (
	merchantOrdersPattern = "GET /merchants/{merchant}/orders"
	// Ingress counts a call under its own path when no migration state lists a pattern.
	merchantOneOrdersRoute = "GET /merchants/1/orders"

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
	ordersScripts = `
		import { ingress } from "spectre";
		import type { MerchantOrders } from "orders";

		ingress.http("GET /merchants/{merchant}/orders").match<MerchantOrders>();
	`
)

func TestMySQLJoinReadsTheRowsOfSeparateSelects(t *testing.T) {
	m := startOrdersMirror(t, selectEachOrdersItems, joinOrdersToItems)

	m.Ingress.Get("/merchants/1/orders").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneOrders)
	// Ingress counts the call once both services answered alike, so the candidate
	// built the same orders from the reference's rows.
	m.ExpectCalls(it.Calls{merchantOneOrdersRoute: 1})
	// Only the reference's statements reach the database, as the server digests them.
	m.MySQL().ExpectSelects(map[string]uint64{
		"SELECT `id` , `label` FROM `orders` WHERE `merchant_id` = ? ORDER BY `id`":        1,
		"SELECT `id` , `sku` , `quantity` FROM `items` WHERE `order_id` = ? ORDER BY `id`": 3,
	})
}

func TestMySQLSeparateSelectsReadTheRowsOfAJoin(t *testing.T) {
	m := startOrdersMirror(t, joinOrdersToItems, selectEachOrdersItems)

	m.Ingress.Get("/merchants/1/orders").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneOrders)
	m.ExpectCalls(it.Calls{merchantOneOrdersRoute: 1})
	m.MySQL().ExpectSelects(map[string]uint64{
		"SELECT `o` . `id` , `o` . `label` , `i` . `id` , `i` . `sku` , `i` . `quantity` FROM `orders` `o` LEFT JOIN `items` `i`" +
			" ON `i` . `order_id` = `o` . `id` WHERE `o` . `merchant_id` = ? ORDER BY `o` . `id` , `i` . `id`": 1,
	})
}

func TestMySQLCandidateLookupTheReferenceNeverMadeQuarantines(t *testing.T) {
	m := startOrdersMirror(t, selectEachOrdersItems, joinOrdersToItems)

	// No reference statement read order 99's items, so the candidate gets an error, not rows.
	_, err := selectItemsOf(t.Context(), m.MySQL().Candidate, 99)
	assert.Contains(t, err.Error(), "SPECTRE: no matching reference rows")
	m.ExpectQuarantine(m.MySQL().UnmatchedLookup("items", "no matching reference request within 500ms"))
	m.MySQL().ExpectSelects(map[string]uint64{})
}

func TestMySQLReferenceLookupTheCandidateNeverMadeQuarantines(t *testing.T) {
	m := startOrdersMirror(t, selectEachOrdersItems, joinOrdersToItems)

	// The reference reads order 7's items, and the candidate never asks for them.
	items, err := selectItemsOf(t.Context(), m.MySQL().Reference, 7)
	assert.NoError(t, err)
	assert.Equal(t, []item{{ID: 1, SKU: Some("apple"), Quantity: 2}, {ID: 2, Quantity: 1}}, items)
	m.ExpectQuarantine(m.MySQL().UnmatchedLookup("items", "no matching candidate request within 500ms"))
}

func TestMySQLReferenceRowsBeyondTheSavedSizeQuarantine(t *testing.T) {
	m := startOrdersMirror(t, selectEachOrdersItems, joinOrdersToItems, it.MySQLSavedRowsBytes(64))

	// The reference is answered in full, although the proxy has no room to save its rows.
	m.Ingress.Get("/merchants/1/orders").ExpectStatus(http.StatusOK).ExpectBodyContains(merchantOneOrders)
	m.ExpectQuarantine(m.MySQL().SavedRowsExceeded("orders"))
}

func TestMySQLReferenceStatementsThatAreNotSavedReachTheDatabase(t *testing.T) {
	m := startOrdersMirror(t, selectEachOrdersItems, joinOrdersToItems)
	reference := m.MySQL().Reference

	// Writes and prepared statements are forwarded as they are, without saving rows.
	_, err := reference.ExecContext(t.Context(), "INSERT INTO items (id, order_id, sku, quantity) VALUES (?, ?, ?, ?)", 5, 8, "fig", 3)
	assert.NoError(t, err)
	prepared, err := reference.PrepareContext(t.Context(), "SELECT sku FROM items WHERE order_id = ?")
	assert.NoError(t, err)
	defer prepared.Close()
	var sku string
	assert.NoError(t, prepared.QueryRowContext(t.Context(), 8).Scan(&sku))
	assert.Equal(t, "fig", sku)

	// A statement the database refuses fails with the database's own error.
	_, err = reference.ExecContext(t.Context(), "SELECT id FROM missing")
	assert.Contains(t, err.Error(), "Error 1146")
}

// startOrdersMirror mirrors two orders services that read one database in their own ways.
func startOrdersMirror(t *testing.T, reference, candidate readOrders, settings ...it.Setting) *it.Mirror {
	t.Helper()
	return it.Start(t, append([]it.Setting{
		it.Schema(ordersSchema),
		it.Scripts(ordersScripts),
		it.MySQL(
			"CREATE TABLE orders (id BIGINT PRIMARY KEY, merchant_id BIGINT NOT NULL, label VARCHAR(32) NOT NULL)",
			"CREATE TABLE items (id BIGINT PRIMARY KEY, order_id BIGINT NOT NULL, sku VARCHAR(32), quantity INT NOT NULL)",
			"INSERT INTO orders VALUES (7, 1, 'A'), (8, 1, 'B'), (9, 1, 'C'), (10, 2, 'D')",
			"INSERT INTO items VALUES (1, 7, 'apple', 2), (2, 7, NULL, 1), (3, 9, 'pear', 5), (4, 10, 'plum', 1)",
		),
		it.Reference(ordersService(reference)),
		it.Candidate(ordersService(candidate)),
	}, settings...)...)
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

// readOrders reads a merchant's orders and their items from the database.
type readOrders func(ctx context.Context, database *sql.DB, merchant int64) ([]order, error)

// ordersService serves a merchant's orders, read from the mirror's database with read.
func ordersService(read readOrders) it.Service {
	return func(t *testing.T, backend it.Backend) {
		t.Helper()
		database := backend.MySQL.MustGet()
		backend.HTTP.HandleFunc(merchantOrdersPattern, func(writer http.ResponseWriter, request *http.Request) {
			merchant, err := strconv.ParseInt(request.PathValue("merchant"), 10, 64)
			if err != nil {
				http.Error(writer, "merchant must be a number", http.StatusBadRequest)
				return
			}
			orders, err := read(request.Context(), database, merchant)
			if err != nil {
				http.Error(writer, "orders cannot be read", http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string][]order{"orders": orders})
		})
	}
}

// selectEachOrdersItems selects the orders, then the items of each order with a statement of its own.
func selectEachOrdersItems(ctx context.Context, database *sql.DB, merchant int64) ([]order, error) {
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

// joinOrdersToItems selects the orders and their items with one statement.
func joinOrdersToItems(ctx context.Context, database *sql.DB, merchant int64) ([]order, error) {
	rows, err := database.QueryContext(ctx, "SELECT o.id, o.label, i.id, i.sku, i.quantity FROM orders o"+
		" LEFT JOIN items i ON i.order_id = o.id WHERE o.merchant_id = ? ORDER BY o.id, i.id", merchant)
	if err != nil {
		return nil, errors.Wrap(err, "select orders and items")
	}
	defer rows.Close()
	orders := []order{}
	for rows.Next() {
		read := order{}
		itemID, sku, quantity := sql.NullInt64{}, sql.NullString{}, sql.NullInt64{}
		if err := rows.Scan(&read.ID, &read.Label, &itemID, &sku, &quantity); err != nil {
			return nil, errors.Wrap(err, "read order and item")
		}
		if len(orders) == 0 || orders[len(orders)-1].ID != read.ID {
			orders = append(orders, read)
		}
		if !itemID.Valid {
			continue
		}
		joined := item{ID: itemID.Int64, Quantity: quantity.Int64}
		if sku.Valid {
			joined.SKU = Some(sku.String)
		}
		last := &orders[len(orders)-1]
		last.Items = append(last.Items, joined)
	}
	return orders, errors.Wrap(rows.Err(), "read orders and items")
}

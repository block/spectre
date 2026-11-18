package mysqlserver

import (
	"strings"
	"sync"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/client"
)

const primaryKeyQuery = "SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE" +
	" WHERE CONSTRAINT_NAME = 'PRIMARY' AND TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION"

// primaryKeys reads each table's primary key from the database once, on a connection of
// its own. Result metadata cannot supply it: MySQL drops key flags from sorted joins.
type primaryKeys struct {
	config Config
	mu     sync.Mutex
	// conn and known are guarded by mu.
	conn  Option[*client.Conn]
	known map[string][]string
}

func newPrimaryKeys(config Config) *primaryKeys {
	return &primaryKeys{config: config, known: map[string][]string{}}
}

// of returns the lower-case primary key columns of a table, in key order.
func (k *primaryKeys) of(read table) ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	name := read.schema + "." + read.name
	if key, ok := k.known[name]; ok {
		return key, nil
	}
	conn, connected := k.conn.Get()
	if !connected {
		opened, err := client.Connect(k.config.Upstream, k.config.User, k.config.Password, "")
		if err != nil {
			return nil, errors.Wrap(err, "connect to read primary keys")
		}
		conn, k.conn = opened, Some(opened)
	}
	result, err := conn.Execute(primaryKeyQuery, read.schema, read.name)
	if err != nil {
		// The connection may be broken, so the next read opens another.
		_ = conn.Close()
		k.conn = None[*client.Conn]()
		return nil, errors.Wrap(err, "read primary key")
	}
	defer result.Close()
	key := make([]string, 0, result.RowNumber())
	for index := range result.RowNumber() {
		column, err := result.GetString(index, 0)
		if err != nil {
			return nil, errors.Wrap(err, "read primary key")
		}
		key = append(key, strings.ToLower(column))
	}
	k.known[name] = key
	return key, nil
}

// close closes the connection, if one is open.
func (k *primaryKeys) close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if conn, ok := k.conn.Get(); ok {
		_ = conn.Close()
		k.conn = None[*client.Conn]()
	}
}

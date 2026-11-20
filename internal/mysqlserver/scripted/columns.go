package scripted

import (
	"context"
	"sync"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

// Database describes the columns of the reply a statement would get, without running it.
type Database interface {
	Describe(ctx context.Context, database, statement string) ([]normaliser.Column, error)
}

// columns are the patterns' columns as the database describes them, by default database.
type columns struct {
	database  Database
	mu        sync.Mutex
	described map[described][]normaliser.Column
}

// described names a pattern described in a default database.
type described struct {
	database, digest string
}

func newColumns(database Database) *columns {
	return &columns{database: database, described: map[described][]normaliser.Column{}}
}

// of returns a statement's columns in a default database, which the database describes
// the first time. Failures are not kept, so the next statement asks again.
func (c *columns) of(ctx context.Context, database, digest, statement string) ([]normaliser.Column, error) {
	key := described{database: database, digest: digest}
	c.mu.Lock()
	found, ok := c.described[key]
	c.mu.Unlock()
	if ok {
		return found, nil
	}
	found, err := c.database.Describe(ctx, database, statement)
	if err != nil {
		return nil, errors.Wrapf(err, "describe the columns of %q", statement)
	}
	c.mu.Lock()
	c.described[key] = found
	c.mu.Unlock()
	return found, nil
}

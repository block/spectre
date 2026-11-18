//go:build integration

package it

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-sql-driver/mysql"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/mysqlserver"
)

const (
	mysqlImage    = "mysql:8.4"
	mysqlUser     = "root"
	mysqlPassword = "spectre"
	// mysqlLifetime removes the server even when a test process dies without stopping it.
	mysqlLifetime = "900"
)

// StartMySQLServer starts the MySQL server that every test of this process shares,
// without waiting for it to accept connections. stop removes it.
func StartMySQLServer(ctx context.Context) (stop func() error, err error) {
	name := mysqlContainer()
	run := exec.CommandContext(ctx, "docker", "run", "--detach", "--rm", "--name", name,
		"--env", "MYSQL_ROOT_PASSWORD="+mysqlPassword, "--publish", "127.0.0.1::3306",
		"--tmpfs", "/var/lib/mysql", "--entrypoint", "timeout", mysqlImage,
		mysqlLifetime, "docker-entrypoint.sh", "mysqld")
	if output, err := run.CombinedOutput(); err != nil {
		return func() error { return nil }, errors.Wrapf(err, "start MySQL: %s", strings.TrimSpace(string(output)))
	}
	return func() error {
		// The test context is done by now, and removal must still run.
		remove := exec.CommandContext(context.WithoutCancel(ctx), "docker", "rm", "--force", name)
		return errors.Wrap(remove.Run(), "remove MySQL")
	}, nil
}

// mysqlContainer names the server after the test process, so tests find it without shared state.
func mysqlContainer() string {
	return fmt.Sprintf("spectre-integration-mysql-%d", os.Getpid())
}

// mysqlAddress returns where the shared MySQL server listens on this host.
func mysqlAddress(t *testing.T) string {
	t.Helper()
	output, err := exec.CommandContext(t.Context(), "docker", "port", mysqlContainer(), "3306/tcp").Output()
	assert.NoError(t, err, "find the MySQL server that TestMain starts; is Docker running?")
	address, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	return address
}

// Database is the MySQL database of a mirror's services, and where each service reaches it.
type Database struct {
	// Name is the test's own database on the shared server.
	Name string
	// Reference and Candidate send statements through the proxy, as each service does.
	Reference *sql.DB
	Candidate *sql.DB
	t         *testing.T
	address   string
	server    *sql.DB
}

// newMySQL creates the test's database on the shared server, once the server accepts
// connections, and runs statements in it.
func newDatabase(t *testing.T, w *watch, statements []string) *Database {
	t.Helper()
	address := mysqlAddress(t)
	server := openMySQL(t, address, "")
	w.await(t, startTimeout, "MySQL to accept connections", func() bool {
		return server.PingContext(t.Context()) == nil
	})
	suffix := make([]byte, 8)
	_, err := rand.Read(suffix)
	assert.NoError(t, err)
	database := fmt.Sprintf("it_%x", suffix)
	_, err = server.ExecContext(t.Context(), "CREATE DATABASE "+database)
	assert.NoError(t, err)
	t.Cleanup(func() {
		if _, err := server.ExecContext(context.WithoutCancel(t.Context()), "DROP DATABASE "+database); err != nil {
			t.Logf("drop %s: %v", database, err)
		}
	})
	seed := openMySQL(t, address, database)
	for _, statement := range statements {
		_, err := seed.ExecContext(t.Context(), statement)
		assert.NoError(t, err, "run %s", statement)
	}
	return &Database{Name: database, t: t, address: address, server: server}
}

// connect opens the handles that reach the database through the proxy's listeners.
func (d *Database) connect(reference, candidate net.Listener) mysqlserver.Config {
	d.Reference = openMySQL(d.t, reference.Addr().String(), d.Name)
	d.Candidate = openMySQL(d.t, candidate.Addr().String(), d.Name)
	config := mysqlserver.NewConfig()
	config.Upstream, config.User, config.Password = d.address, mysqlUser, mysqlPassword
	config.ReferenceListen, config.CandidateListen = reference.Addr().String(), candidate.Addr().String()
	return config
}

// ExpectSelects checks the SELECT statements the database server itself ran in the
// test's database so far, as counts by the server's statement digest.
func (d *Database) ExpectSelects(expected map[string]uint64) {
	d.t.Helper()
	rows, err := d.server.QueryContext(d.t.Context(),
		"SELECT DIGEST_TEXT, COUNT_STAR FROM performance_schema.events_statements_summary_by_digest"+
			" WHERE SCHEMA_NAME = ? AND DIGEST_TEXT LIKE 'SELECT %'", d.Name)
	assert.NoError(d.t, err)
	defer rows.Close()
	selects := map[string]uint64{}
	for rows.Next() {
		var digest string
		var count uint64
		assert.NoError(d.t, rows.Scan(&digest, &count))
		selects[digest] = count
	}
	assert.NoError(d.t, rows.Err())
	assert.Equal(d.t, expected, selects)
}

// UnmatchedLookup is the reason for a MySQL lookup of table in the mirror's database
// that no lookup from the other service matched.
func (d *Database) UnmatchedLookup(table string, detail string) model.QuarantineReason {
	return d.reason(model.QuarantineUnmatched, table, detail)
}

// SavedRowsExceeded is the reason for reference rows of table that the proxy had no room to save.
func (d *Database) SavedRowsExceeded(table string) model.QuarantineReason {
	return d.reason(model.QuarantineCapacity, table, "saved reference rows exceed the MySQL saved rows size")
}

func (d *Database) reason(kind model.QuarantineKind, table string, detail string) model.QuarantineReason {
	return model.QuarantineReason{
		Kind:    kind,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: mysqlserver.Protocol, Route: model.Route("SELECT " + d.Name + "." + table)}),
		Detail:  detail,
	}
}

// openMySQL opens a handle to database at address, closed when the test ends. Values
// are written into the statement text, because the proxy answers only text statements.
func openMySQL(t *testing.T, address, database string) *sql.DB {
	t.Helper()
	config := mysql.NewConfig()
	config.User, config.Passwd, config.Net, config.Addr, config.DBName = mysqlUser, mysqlPassword, "tcp", address, database
	config.InterpolateParams = true
	// The driver logs every failed attempt while the server starts.
	config.Logger = &mysql.NopLogger{}
	connector, err := mysql.NewConnector(config)
	assert.NoError(t, err)
	handle := sql.OpenDB(connector)
	t.Cleanup(func() { _ = handle.Close() })
	return handle
}

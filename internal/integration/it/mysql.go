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
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/client"
	"github.com/go-sql-driver/mysql"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocols/mysql/scripted"
)

const (
	mysqlImage    = "mysql:8.4"
	mysqlUser     = "root"
	mysqlPassword = "spectre"
	// mysqlLifetime removes the server even when a test process dies without removing it.
	mysqlLifetime = "900"
)

// RemoveMySQLServer removes the MySQL server that the tests of this process shared,
// if any test started it.
func RemoveMySQLServer() {
	remove := exec.CommandContext(context.Background(), "docker", "rm", "--force", mysqlContainer())
	if output, err := remove.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "remove MySQL: %v: %s\n", err, output)
	}
}

// startMySQL starts the MySQL server that the tests of this process share, unless
// another test already did, and returns where it listens on this host.
func startMySQL(t *testing.T, w *watch) string {
	t.Helper()
	name := mysqlContainer()
	run := exec.CommandContext(t.Context(), "docker", "run", "--detach", "--rm", "--name", name,
		"--env", "MYSQL_ROOT_PASSWORD="+mysqlPassword, "--publish", "127.0.0.1::3306", "--tmpfs", "/var/lib/mysql",
		"--entrypoint", "timeout", mysqlImage, mysqlLifetime, "docker-entrypoint.sh", "mysqld", "--require-secure-transport=ON")
	if output, err := run.CombinedOutput(); err != nil && !strings.Contains(string(output), "is already in use") {
		t.Fatalf("start MySQL; is Docker running? %v: %s", err, output)
	}
	address := ""
	// The test that won the race to start the server may not have published its port yet.
	w.await(t, startTimeout, "MySQL to publish its port", func() bool {
		output, err := exec.CommandContext(t.Context(), "docker", "port", name, "3306/tcp").Output()
		address, _, _ = strings.Cut(strings.TrimSpace(string(output)), "\n")
		return err == nil && address != ""
	})
	return address
}

// mysqlContainer names the server after the test process, so its tests find it
// without shared state, and other test processes never do.
func mysqlContainer() string {
	return fmt.Sprintf("spectre-integration-mysql-%d", os.Getpid())
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
	// referenceListen is where the reference reaches the proxy.
	referenceListen string
	// user is the test's own database user, which the proxy connects as with the
	// password in passwordFile.
	user         string
	passwordFile string
	// authorities is the file of the certificate authorities that the server generated.
	authorities string
	server      *sql.DB
	watch       *watch
	// seeded counts the statements that set the database up.
	seeded map[string]uint64
}

// newDatabase creates the test's own database on the shared server, once the server
// accepts connections, and runs statements in it.
func newDatabase(t *testing.T, w *watch, statements []string) *Database {
	t.Helper()
	address := startMySQL(t, w)
	server := openMySQL(t, address, "")
	w.await(t, startTimeout, "MySQL to accept connections", func() bool {
		return server.PingContext(t.Context()) == nil
	})
	suffix := make([]byte, 8)
	_, err := rand.Read(suffix)
	assert.NoError(t, err)
	database := fmt.Sprintf("it_%x", suffix)
	for _, statement := range []string{
		"CREATE DATABASE " + database,
		fmt.Sprintf("CREATE USER %s IDENTIFIED BY '%s'", database, mysqlPassword),
		fmt.Sprintf("GRANT ALL ON %s.* TO %s", database, database),
	} {
		_, err = server.ExecContext(t.Context(), statement)
		assert.NoError(t, err, "run %s", statement)
	}
	seed := openMySQL(t, address, database)
	for _, statement := range statements {
		_, err := seed.ExecContext(t.Context(), statement)
		assert.NoError(t, err, "run %s", statement)
	}
	return &Database{
		Name: database, t: t, address: address, authorities: authorities(t), server: server, watch: w, user: database,
		passwordFile: filepath.Join(t.TempDir(), "password"),
		seeded:       digests(t, server, database),
	}
}

// authorities copies the certificate authorities that the shared server generated
// into a file of the test's own.
func authorities(t *testing.T) string {
	t.Helper()
	output, err := exec.CommandContext(t.Context(), "docker", "exec", mysqlContainer(), "cat", "/var/lib/mysql/ca.pem").Output()
	assert.NoError(t, err)
	path := filepath.Join(t.TempDir(), "ca.pem")
	assert.NoError(t, os.WriteFile(path, output, 0o600))
	return path
}

// connect opens the handles that reach the database through the proxy's listeners.
func (d *Database) connect(reference, candidate net.Listener) []string {
	d.referenceListen = reference.Addr().String()
	d.Reference = openMySQL(d.t, d.referenceListen, d.Name)
	d.Candidate = openMySQL(d.t, candidate.Addr().String(), d.Name)
	d.SetProxyPassword(mysqlPassword)
	return []string{
		"--mysql-upstream=" + d.address,
		"--mysql-upstream-ca=" + d.authorities,
		"--mysql-upstream-user=" + d.user,
		"--mysql-upstream-password-file=" + d.passwordFile,
		"--mysql-reference-listen=" + reference.Addr().String(),
		"--mysql-candidate-listen=" + candidate.Addr().String(),
	}
}

// ExpectStatements checks the statements the database server itself ran in the
// test's database since it was set up, as counts by the server's statement digest.
func (d *Database) ExpectStatements(expected map[string]uint64) {
	d.t.Helper()
	ran := digests(d.t, d.server, d.Name)
	for digest, count := range d.seeded {
		ran[digest] -= count
		if ran[digest] == 0 {
			delete(ran, digest)
		}
	}
	assert.Equal(d.t, expected, ran)
}

// digests counts the statements the server ran in database, by statement digest.
func digests(t *testing.T, server *sql.DB, database string) map[string]uint64 {
	t.Helper()
	rows, err := server.QueryContext(t.Context(),
		"SELECT DIGEST_TEXT, COUNT_STAR FROM performance_schema.events_statements_summary_by_digest WHERE SCHEMA_NAME = ?", database)
	assert.NoError(t, err)
	defer rows.Close()
	counts := map[string]uint64{}
	for rows.Next() {
		var digest string
		var count uint64
		assert.NoError(t, rows.Scan(&digest, &count))
		counts[digest] = count
	}
	assert.NoError(t, rows.Err())
	return counts
}

// MySQLQuarantine is the reason for MySQL traffic of route that quarantined the candidate.
func MySQLQuarantine(kind model.QuarantineKind, route string, detail string) model.QuarantineReason {
	return model.QuarantineReason{
		Kind:    kind,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: scripted.Protocol, Route: model.Route(route)}),
		Detail:  detail,
	}
}

// SetProxyPassword writes the password that the proxy connects to the database with.
func (d *Database) SetProxyPassword(password string) {
	assert.NoError(d.t, os.WriteFile(d.passwordFile, []byte(password+"\n"), 0o600))
}

// RotatePassword changes the password of the test's database user on the server.
func (d *Database) RotatePassword(password string) {
	_, err := d.server.ExecContext(d.t.Context(), fmt.Sprintf("ALTER USER %s IDENTIFIED BY '%s'", d.user, password))
	assert.NoError(d.t, err)
}

// OpenReference opens a new handle that reaches the database through the proxy, as
// the reference does, as a user the database does not know. It is closed when the test ends.
func (d *Database) OpenReference() *sql.DB {
	return openMySQLAs(d.t, d.referenceListen, d.Name, "anyone", "anything")
}

// ConnectReference opens a protocol connection to the proxy as the reference, as a user
// the database does not know. It is closed when the test ends.
func (d *Database) ConnectReference(options ...client.Option) *client.Conn {
	d.t.Helper()
	conn, err := client.ConnectWithContext(d.t.Context(), d.referenceListen, "anyone", "anything", d.Name, waitTimeout, options...)
	assert.NoError(d.t, err)
	d.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// Server is a handle to the server itself, bypassing the proxy.
func (d *Database) Server() *sql.DB {
	return d.server
}

// AwaitRunning waits until the server runs statement.
func (d *Database) AwaitRunning(statement string) {
	d.t.Helper()
	d.watch.await(d.t, waitTimeout, "MySQL to run "+statement, func() bool {
		running := 0
		err := d.server.QueryRowContext(d.t.Context(),
			"SELECT COUNT(*) FROM information_schema.processlist WHERE info = ?", statement).Scan(&running)
		return err == nil && running > 0
	})
}

// openMySQL opens a handle to database at address, closed when the test ends.
func openMySQL(t *testing.T, address, database string) *sql.DB {
	t.Helper()
	return openMySQLAs(t, address, database, mysqlUser, mysqlPassword)
}

// openMySQLAs opens a handle to database at address as user. Values are written into
// the statement text, because the proxy answers only text statements.
func openMySQLAs(t *testing.T, address, database, user, password string) *sql.DB {
	t.Helper()
	config := mysql.NewConfig()
	config.User, config.Passwd, config.Net, config.Addr, config.DBName = user, password, "tcp", address, database
	config.InterpolateParams = true
	// The server requires TLS, which the proxy's local listeners do not offer.
	config.TLSConfig = "preferred"
	// The driver logs every failed attempt while the server starts.
	config.Logger = &mysql.NopLogger{}
	connector, err := mysql.NewConnector(config)
	assert.NoError(t, err)
	handle := sql.OpenDB(connector)
	t.Cleanup(func() { _ = handle.Close() })
	return handle
}

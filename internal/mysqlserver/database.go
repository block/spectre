package mysqlserver

import (
	"context"
	"crypto/tls"
	"os"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/client"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

// Database is the database that reference connections reach, with the proxy's credentials.
type Database struct {
	address      string
	user         string
	passwordFile string
	tls          Option[*tls.Config]
}

// NewDatabase reads how to reach the configured database.
func NewDatabase(config Config) (*Database, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !config.Enabled() {
		return nil, errors.New("MySQL upstream is required")
	}
	authorities, err := upstreamTLS(config.UpstreamCA)
	if err != nil {
		return nil, err
	}
	return &Database{address: config.Upstream, user: config.UpstreamUser, passwordFile: config.UpstreamPasswordFile, tls: authorities}, nil
}

// Describe returns the columns of the reply that a statement would get in database, by
// preparing it without running it.
func (d *Database) Describe(ctx context.Context, database, statement string) ([]normaliser.Column, error) {
	conn, err := d.dial(ctx, database)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Preparing takes no context, so closing the connection ends it.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	prepared, err := conn.Prepare(statement)
	if err != nil {
		return nil, errors.Wrapf(err, "prepare %q", statement)
	}
	defer prepared.Close()
	columns := make([]normaliser.Column, 0, len(prepared.RawColumnFields))
	for _, raw := range prepared.RawColumnFields {
		field := &mysql.Field{}
		if err := field.Parse(raw); err != nil {
			return nil, errors.Wrapf(err, "read the columns of %q", statement)
		}
		columns = append(columns, columnOf(field))
	}
	return columns, nil
}

// dial connects to the database with the proxy's credentials, and selects database
// if one is named. The password file is read for every connection, so it can be rotated.
func (d *Database) dial(ctx context.Context, database string, options ...client.Option) (*client.Conn, error) {
	password, err := os.ReadFile(d.passwordFile)
	if err != nil {
		return nil, errors.Wrap(err, "read the MySQL password file")
	}
	if config, ok := d.tls.Get(); ok {
		options = append(options, func(c *client.Conn) error {
			c.SetTLSConfig(config)
			return nil
		})
	}
	upstream, err := client.ConnectWithContext(ctx, d.address, d.user, strings.TrimRight(string(password), "\r\n"), database,
		dialTimeout, options...)
	return upstream, errors.WithStack(err)
}

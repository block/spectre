package mysqlserver

import (
	"crypto/tls"
	"crypto/x509"
	"os"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"
)

// anyUser asks every user for a password with its plugin, and leaves checking the
// password to the server's authentication provider.
type anyUser struct {
	plugin string
}

func newAnyUser(plugin string) anyUser {
	return anyUser{plugin: plugin}
}

// GetCredential finds every user.
func (a anyUser) GetCredential(_ string) (credential server.Credential, found bool, err error) {
	// The wire library needs a password, which the provider never compares.
	return server.Credential{Passwords: []string{"unused"}, AuthPluginName: a.plugin}, true, nil
}

// OnAuthSuccess accepts every authenticated connection.
func (anyUser) OnAuthSuccess(_ *server.Conn) error {
	return nil
}

// OnAuthFailure leaves reporting failures to the provider.
func (anyUser) OnAuthFailure(_ *server.Conn, _ error) {}

// acceptAny authenticates every candidate connection, which never reaches a database.
type acceptAny struct{}

// Authenticate accepts any password.
func (acceptAny) Authenticate(_ *server.Conn, _ string, _ []byte) error {
	return nil
}

// Validate supports native passwords, which need neither TLS nor a key pair.
func (acceptAny) Validate(plugin string) bool {
	return plugin == mysql.AUTH_NATIVE_PASSWORD
}

// upstreamTLS is how the proxy connects to the database: with TLS when path names
// its certificate authorities.
func upstreamTLS(path string) (Option[*tls.Config], error) {
	if path == "" {
		return None[*tls.Config](), nil
	}
	authorities, err := os.ReadFile(path)
	if err != nil {
		return None[*tls.Config](), errors.Wrap(err, "read the MySQL certificate authorities")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(authorities) {
		return None[*tls.Config](), errors.Errorf("%s has no PEM certificates", path)
	}
	return Some(&tls.Config{
		MinVersion: tls.VersionTLS12,
		// Services name the database by an alias its certificate lacks, as MySQL clients do with a CA alone.
		InsecureSkipVerify: true, //nolint:gosec // VerifyConnection verifies the chain.
		VerifyConnection: func(state tls.ConnectionState) error {
			return verifyChain(roots, state.PeerCertificates)
		},
	}), nil
}

// verifyChain checks that the first of certificates chains to roots through the rest.
func verifyChain(roots *x509.CertPool, certificates []*x509.Certificate) error {
	if len(certificates) == 0 {
		return errors.New("MySQL server sent no certificate")
	}
	intermediates := x509.NewCertPool()
	for _, certificate := range certificates[1:] {
		intermediates.AddCert(certificate)
	}
	_, err := certificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates})
	return errors.Wrap(err, "verify the MySQL server certificate")
}

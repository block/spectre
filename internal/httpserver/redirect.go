package httpserver

import (
	"net"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/netaddr"
)

// tlsHandshake is the record type that opens every TLS connection.
const tlsHandshake = 0x16

// redirectListener accepts the candidate's connections that iptables redirected
// to egress, and passes each one's first bytes to reject unless they are HTTP.
type redirectListener struct {
	net.Listener
	reject func(destination string, protocol string)
}

// newRedirectListener passes the destination and protocol of every connection
// whose first bytes are not HTTP to reject.
func newRedirectListener(listener net.Listener, reject func(destination string, protocol string)) redirectListener {
	return redirectListener{Listener: listener, reject: reject}
}

// Accept returns the next connection, tagged with where the candidate sent it.
func (l redirectListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err //nolint:wrapcheck // http.Server inspects the listener's own error.
	}
	return newRedirectedConn(conn, l.reject), nil
}

// redirectedConn fails its first read, after reporting the destination, when the
// candidate sends something other than HTTP or nothing before the read deadline.
type redirectedConn struct {
	net.Conn
	destination string
	reject      func(destination string, protocol string)
	// checked is set once the first read has returned.
	checked bool
}

// newRedirectedConn checks the first bytes of conn, which iptables redirected.
func newRedirectedConn(conn net.Conn, reject func(destination string, protocol string)) *redirectedConn {
	return &redirectedConn{Conn: conn, destination: netaddr.OriginalDestination(conn), reject: reject}
}

// Read reads from the connection, checking the protocol of the first bytes.
func (c *redirectedConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	if c.checked || (n == 0 && err == nil) {
		return n, err //nolint:wrapcheck // http.Server inspects the connection's own error.
	}
	c.checked = true
	protocol, rejected := sniff(buffer[:n], err)
	if !rejected {
		return n, err //nolint:wrapcheck // http.Server inspects the connection's own error.
	}
	c.reject(c.destination, protocol)
	return 0, errors.Errorf("candidate sent %s to %s", protocol, c.destination)
}

// sniff names what a connection's first read returned, unless it may be HTTP.
// Every HTTP method, and the HTTP/2 preface, starts with an uppercase letter.
func sniff(first []byte, err error) (protocol string, rejected bool) {
	netErr, isNetErr := errors.AsType[net.Error](err)
	switch {
	case len(first) == 0 && isNetErr && netErr.Timeout():
		return "nothing", true
	case len(first) == 0:
		return "", false
	case first[0] == tlsHandshake:
		return "TLS", true
	case first[0] < 'A' || first[0] > 'Z':
		return "data other than HTTP", true
	}
	return "", false
}

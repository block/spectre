package netaddr

import "net"

// OriginalDestination returns the address a connection was sent to before an
// iptables redirect, or its local address when it was not redirected.
func OriginalDestination(conn net.Conn) string {
	if destination, ok := redirectedDestination(conn); ok {
		return destination
	}
	return conn.LocalAddr().String()
}

//go:build !linux

package netaddr

import "net"

// redirectedDestination reports that iptables redirects exist only on Linux.
func redirectedDestination(_ net.Conn) (destination string, ok bool) {
	return "", false
}

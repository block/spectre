//go:build linux

package netaddr

import (
	"encoding/binary"
	"net"
	"net/netip"

	"golang.org/x/sys/unix"
)

// redirectedDestination reads the IPv4 destination that conntrack recorded
// before an iptables REDIRECT rewrote it.
func redirectedDestination(conn net.Conn) (destination string, ok bool) {
	tcp, isTCP := conn.(*net.TCPConn)
	if !isTCP {
		return "", false
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return "", false
	}
	var address *unix.IPv6Mreq
	var optionErr error
	err = raw.Control(func(fd uintptr) {
		// The option fills a sockaddr_in, which is the size of an IPv6Mreq.
		address, optionErr = unix.GetsockoptIPv6Mreq(int(fd), unix.SOL_IP, unix.SO_ORIGINAL_DST) //nolint:gosec // File descriptors fit in an int.
	})
	if err != nil || optionErr != nil {
		return "", false
	}
	// A sockaddr_in holds the family, then the port and IPv4 address in network order.
	port := binary.BigEndian.Uint16(address.Multiaddr[2:4])
	ip := netip.AddrFrom4([4]byte(address.Multiaddr[4:8]))
	return netip.AddrPortFrom(ip, port).String(), true
}

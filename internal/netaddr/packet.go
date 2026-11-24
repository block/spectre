package netaddr

import (
	"context"
	"net"
	"strings"

	"github.com/alecthomas/errors"
)

const (
	networkUDP      = "udp"
	networkUnixgram = "unixgram"
)

// PacketEndpoint is a parsed datagram address: a UDP host:port or a unix datagram socket.
type PacketEndpoint struct {
	network string
	address string
}

// ParsePacket parses a datagram address. A "unix:" prefix selects a unix datagram
// socket (absolute path or "@" abstract name); anything else is a UDP host:port.
func ParsePacket(value string) *PacketEndpoint {
	if socket, ok := strings.CutPrefix(value, unixPrefix); ok {
		return &PacketEndpoint{network: networkUnixgram, address: socket}
	}
	return &PacketEndpoint{network: networkUDP, address: value}
}

// Listen binds a datagram socket for the endpoint, clearing a stale unix socket
// so a restart can rebind it.
func (e *PacketEndpoint) Listen(ctx context.Context) (net.PacketConn, error) {
	if e.IsUnix() {
		if err := removeStaleSocket(e.address); err != nil {
			return nil, errors.Wrap(err, "prepare unix socket")
		}
	}
	conn, err := (&net.ListenConfig{}).ListenPacket(ctx, e.network, e.address)
	if err != nil {
		return nil, errors.Wrap(err, "open datagram socket")
	}
	if e.IsUnix() {
		if err := shareSocket(e.address); err != nil {
			return nil, errors.Join(err, conn.Close())
		}
	}
	return conn, nil
}

// Dial opens a datagram socket that sends to the endpoint.
func (e *PacketEndpoint) Dial(ctx context.Context) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, e.network, e.address)
	return conn, errors.Wrapf(err, "dial %s %s", e.network, e.address)
}

// IsUnix reports whether the endpoint is a unix datagram socket.
func (e *PacketEndpoint) IsUnix() bool {
	return e.network == networkUnixgram
}

// Address reports the unix socket or the UDP host:port.
func (e *PacketEndpoint) Address() string {
	return e.address
}

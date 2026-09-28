// Package listen opens stream listeners for TCP or unix socket addresses.
package listen

import (
	"context"
	"net"
	"os"
	"strings"

	"github.com/alecthomas/errors"
)

const (
	unixPrefix  = "unix:"
	networkUnix = "unix"
	networkTCP  = "tcp"
)

// Listen binds a stream listener for address: a "unix:" prefix selects a unix
// socket (absolute path or "@" abstract name), otherwise a TCP host:port.
func Listen(ctx context.Context, address string) (net.Listener, error) {
	network, target := networkAddress(address)
	if network == networkUnix {
		if err := removeStaleSocket(target); err != nil {
			return nil, errors.Wrap(err, "prepare unix socket")
		}
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, network, target)
	return listener, errors.Wrap(err, "open listener")
}

// networkAddress splits a listen value into a network and address.
func networkAddress(address string) (network string, target string) {
	if socket, ok := strings.CutPrefix(address, unixPrefix); ok {
		return networkUnix, socket
	}
	return networkTCP, address
}

// removeStaleSocket clears a leftover path socket so a restart can bind it.
// Abstract sockets begin with "@" and need no filesystem cleanup.
func removeStaleSocket(address string) error {
	if strings.HasPrefix(address, "@") {
		return nil
	}
	info, err := os.Lstat(address)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "inspect socket path")
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.Errorf("refusing to remove non-socket file %q", address)
	}
	return errors.Wrap(os.Remove(address), "remove stale socket")
}

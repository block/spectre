// Package netaddr parses listen and backend addresses and opens or dials them.
package netaddr

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
)

const (
	schemeHTTP    = "http"
	schemeHTTPS   = "https"
	schemeH2C     = "h2c"
	networkUnix   = "unix"
	networkTCP    = "tcp"
	unixPrefix    = "unix:"
	hostLocalhost = "localhost"
)

// Endpoint is a parsed listen or backend address. Backend endpoints also carry
// a target URL, an h2c flag, and a resolved port.
type Endpoint struct {
	network string
	address string
	url     *url.URL
	h2c     bool
	port    uint16
}

func newEndpoint(network string, address string, target *url.URL, h2c bool, port uint16) *Endpoint {
	return &Endpoint{network: network, address: address, url: target, h2c: h2c, port: port}
}

// ParseListen parses a listener address. A "unix:" prefix selects a unix socket
// (absolute path or "@" abstract name); anything else is a TCP host:port.
func ParseListen(value string) *Endpoint {
	if socket, ok := strings.CutPrefix(value, unixPrefix); ok {
		return newEndpoint(networkUnix, socket, nil, false, 0)
	}
	return newEndpoint(networkTCP, value, nil, false, 0)
}

// ParseBackend parses a backend URL: http, https, or h2c over TCP, or
// "http+unix:"/"h2c+unix:" over a unix socket (absolute path or "@" name).
func ParseBackend(value string) (*Endpoint, error) {
	if scheme, socket, ok := splitUnixScheme(value); ok {
		return parseUnixBackend(scheme, socket, value)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, errors.Wrap(err, "parse backend URL")
	}
	h2c := parsed.Scheme == schemeH2C
	if (parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS && !h2c) || parsed.Host == "" {
		return nil, errors.Errorf("backend URL must be an absolute HTTP, HTTPS, or h2c URL: %q", value)
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.RawQuery != "" || parsed.ForceQuery {
		return nil, errors.Errorf("backend URL cannot contain user information, a query, or a fragment: %q", value)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.Errorf("backend URL cannot contain a path: %q", value)
	}
	parsed.Path = ""
	if h2c {
		parsed.Scheme = schemeHTTP
	}
	port, err := backendPort(parsed, value)
	if err != nil {
		return nil, err
	}
	return newEndpoint(networkTCP, parsed.Host, parsed, h2c, port), nil
}

// parseUnixBackend builds a backend reached over a unix socket. The socket must
// be an absolute path or an abstract name beginning with "@".
func parseUnixBackend(scheme string, socket string, value string) (*Endpoint, error) {
	if !strings.HasPrefix(socket, "/") && !strings.HasPrefix(socket, "@") {
		return nil, errors.Errorf("unix backend socket must be an absolute path or an abstract name beginning with @: %q", value)
	}
	target := &url.URL{Scheme: schemeHTTP, Host: hostLocalhost}
	return newEndpoint(networkUnix, socket, target, scheme == schemeH2C, 0), nil
}

// splitUnixScheme reports the scheme and socket for the "<scheme>+unix:" forms.
// ok is false when value uses no unix scheme.
func splitUnixScheme(value string) (scheme string, socket string, ok bool) {
	for _, candidate := range []string{schemeHTTP, schemeH2C} {
		if trimmed, found := strings.CutPrefix(value, candidate+"+unix:"); found {
			return candidate, trimmed, true
		}
	}
	return "", "", false
}

// backendPort resolves the backend port, defaulting to 80 or 443 by scheme.
func backendPort(parsed *url.URL, value string) (uint16, error) {
	portText := parsed.Port()
	if portText == "" {
		if strings.HasSuffix(parsed.Host, ":") {
			return 0, errors.Errorf("backend URL has an invalid port: %q", value)
		}
		if parsed.Scheme == schemeHTTPS {
			return 443, nil
		}
		return 80, nil
	}
	portNumber, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || portNumber == 0 {
		return 0, errors.Errorf("backend URL has an invalid port: %q", value)
	}
	return uint16(portNumber), nil
}

// Listen binds a stream listener for the endpoint, clearing a stale unix socket
// so a restart can rebind it.
func (e *Endpoint) Listen(ctx context.Context) (net.Listener, error) {
	if e.network == networkUnix {
		if err := removeStaleSocket(e.address); err != nil {
			return nil, errors.Wrap(err, "prepare unix socket")
		}
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, e.network, e.address)
	return listener, errors.Wrap(err, "open listener")
}

// DialContext dials the endpoint's unix socket, ignoring the requested network
// and address so a transport reaches this backend regardless of the URL host.
func (e *Endpoint) DialContext(ctx context.Context, _ string, _ string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, networkUnix, e.address)
	return conn, errors.Wrap(err, "dial unix socket")
}

// Network reports the listener network, "tcp" or "unix".
func (e *Endpoint) Network() string {
	return e.network
}

// Address reports the listener or dial address: a unix socket or a TCP host:port.
func (e *Endpoint) Address() string {
	return e.address
}

// IsUnix reports whether the endpoint is reached over a unix domain socket.
func (e *Endpoint) IsUnix() bool {
	return e.network == networkUnix
}

// IsH2C reports whether the backend speaks unencrypted HTTP/2.
func (e *Endpoint) IsH2C() bool {
	return e.h2c
}

// Socket returns the unix socket path, or an empty string for a TCP endpoint.
func (e *Endpoint) Socket() string {
	if e.IsUnix() {
		return e.address
	}
	return ""
}

// URL returns the backend target URL, or nil for a listener endpoint.
func (e *Endpoint) URL() *url.URL {
	return e.url
}

// Port returns the resolved backend TCP port.
func (e *Endpoint) Port() uint16 {
	return e.port
}

// IsLoopback reports whether the backend or TCP listener host is a loopback IP address.
func (e *Endpoint) IsLoopback() bool {
	var host string
	switch {
	case e.url != nil:
		host = e.url.Hostname()
	case e.network == networkTCP:
		listenHost, _, err := net.SplitHostPort(e.address)
		if err != nil {
			return false
		}
		host = listenHost
	default:
		return false
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.Unmap().IsLoopback()
}

// SameDestination reports whether two backends reach the same destination,
// comparing unix sockets exactly and TCP by scheme, canonical host, and port.
func (e *Endpoint) SameDestination(other *Endpoint) bool {
	return e.identity() == other.identity()
}

func (e *Endpoint) identity() string {
	if e.IsUnix() {
		return "unix://" + e.Socket()
	}
	host := strings.ToLower(e.URL().Hostname())
	if address, err := netip.ParseAddr(host); err == nil {
		host = address.Unmap().String()
	}
	return strings.ToLower(e.URL().Scheme) + "://" + net.JoinHostPort(host, strconv.Itoa(int(e.Port())))
}

// TargetsListener reports whether the backend would loop traffic back to the
// listener, matching unix sockets exactly and TCP by host and port.
func (e *Endpoint) TargetsListener(listener *Endpoint) bool {
	if listener.IsUnix() || e.IsUnix() {
		return listener.IsUnix() && e.IsUnix() && e.Socket() == listener.Address()
	}
	host, port, err := net.SplitHostPort(listener.Address())
	if err != nil {
		return false
	}
	var portNumber uint16
	if number, err := strconv.ParseUint(port, 10, 16); err == nil {
		portNumber = uint16(number)
	} else if number, err := net.DefaultResolver.LookupPort(context.Background(), networkTCP, port); err == nil && number >= 0 && number <= 65535 {
		portNumber = uint16(number)
	}
	if portNumber != e.Port() {
		return false
	}
	target, targetError := netip.ParseAddr(e.URL().Hostname())
	targetIsLocalhost := strings.EqualFold(e.URL().Hostname(), hostLocalhost)
	if targetError != nil && !targetIsLocalhost {
		return false
	}
	if host == "" {
		return true
	}
	listenAddress, err := netip.ParseAddr(host)
	if err != nil {
		return strings.EqualFold(host, hostLocalhost) && (targetIsLocalhost || target.Unmap().IsLoopback())
	}
	if targetIsLocalhost {
		return listenAddress.IsUnspecified() || listenAddress.Unmap().IsLoopback()
	}
	return listenAddress.IsUnspecified() || listenAddress.Unmap() == target.Unmap()
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

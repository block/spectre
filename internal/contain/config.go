// Package contain blocks a user's IP traffic with the kernel's packet filter, so
// a candidate in its own container reaches dependencies only through unix sockets
// or the local ports that its redirected connections reach.
package contain

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
)

// Config contains the command-line configuration of the user to contain, and of the
// connections to redirect.
type Config struct {
	// UID is the user whose IP traffic is rejected, such as the candidate's.
	UID uint32 `name:"uid" required:"" placeholder:"UID" help:"User ID whose IPv4 and IPv6 traffic is rejected, such as the candidate container's. Only replies on connections opened to it, and its redirected connections, still pass; unix sockets are unaffected."`
	// Redirects send connections that would leave the pod to local listeners instead.
	Redirects []Redirect `name:"redirect" placeholder:"UID:PORT=LOCAL_PORT" help:"Send user UID's TCP connections to PORT on any address to LOCAL_PORT on loopback instead, such as the reference's and candidate's MySQL connections to the SPECTRE proxy. Repeat it for each user and port."`
}

// Redirect sends one user's TCP connections to a port to a loopback port instead.
type Redirect struct {
	// UID is the user whose connections are redirected.
	UID uint32
	// Port is the destination port of the connections, on any address.
	Port uint16
	// LocalPort is the loopback port that the connections reach instead.
	LocalPort uint16
}

// UnmarshalText parses UID:PORT=LOCAL_PORT.
func (r *Redirect) UnmarshalText(text []byte) error {
	user, ports, found := strings.Cut(string(text), ":")
	port, local, hasLocal := strings.Cut(ports, "=")
	if !found || !hasLocal {
		return errors.Errorf("redirect %q is not UID:PORT=LOCAL_PORT", text)
	}
	uid, err := strconv.ParseUint(user, 10, 32)
	if err != nil {
		return errors.Errorf("redirect %q has an invalid user ID", text)
	}
	parsed := Redirect{UID: uint32(uid)}
	for _, field := range []struct {
		value  string
		target *uint16
	}{{port, &parsed.Port}, {local, &parsed.LocalPort}} {
		number, err := strconv.ParseUint(field.value, 10, 16)
		if err != nil || number == 0 {
			return errors.Errorf("redirect %q has a port that is not between 1 and 65535", text)
		}
		*field.target = uint16(number)
	}
	*r = parsed
	return nil
}

// String formats the redirect as UID:PORT=LOCAL_PORT.
func (r Redirect) String() string {
	return fmt.Sprintf("%d:%d=%d", r.UID, r.Port, r.LocalPort)
}

// Validate checks that no rule names root, which would contain or redirect every
// process running as root, and that each user's port is redirected once.
func (c Config) Validate() error {
	if c.UID == 0 {
		return errors.New("uid must not be 0, which would contain every process running as root")
	}
	redirected := map[[2]uint32]bool{}
	for _, redirect := range c.Redirects {
		if redirect.UID == 0 {
			return errors.Errorf("redirect %s must not name uid 0, which would redirect every process running as root", redirect)
		}
		key := [2]uint32{redirect.UID, uint32(redirect.Port)}
		if redirected[key] {
			return errors.Errorf("redirect %s repeats a user's port", redirect)
		}
		redirected[key] = true
	}
	return nil
}

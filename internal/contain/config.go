// Package contain blocks a user's IP traffic with the kernel's packet filter, so
// a candidate in its own container reaches dependencies only through unix sockets.
package contain

import "github.com/alecthomas/errors"

// Config contains the command-line configuration of the user to contain.
type Config struct {
	// UID is the user whose IP traffic is rejected, such as the candidate's.
	UID uint32 `name:"uid" required:"" placeholder:"UID" help:"User ID whose IPv4 and IPv6 traffic is rejected, such as the candidate container's. Only replies on connections opened to it still pass; unix sockets are unaffected."`
}

// Validate checks that the user is not root, which would contain every process
// running as root in the pod.
func (c Config) Validate() error {
	if c.UID == 0 {
		return errors.New("uid must not be 0, which would contain every process running as root")
	}
	return nil
}

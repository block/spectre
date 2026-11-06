//go:build !linux

package contain

import "github.com/alecthomas/errors"

// install reports that the packet filter rules need Linux.
func install(_ uint32) error {
	return errors.New("containing a user's IP traffic requires Linux")
}

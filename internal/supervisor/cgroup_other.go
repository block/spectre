//go:build !linux

package supervisor

import "github.com/alecthomas/errors"

// cgroupsSupported reports whether a cgroup v2 hierarchy is available.
func cgroupsSupported() bool { return false }

// cgroupBaseDir reports that cgroups are unavailable off Linux.
func cgroupBaseDir(_ string) (string, error) {
	return "", errors.New("cgroup limits require Linux")
}

// systemMemoryBytes reports that memory detection is unavailable off Linux.
func systemMemoryBytes() (int64, error) {
	return 0, errors.New("system memory detection requires Linux")
}

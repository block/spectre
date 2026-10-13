package supervisor

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
)

const cgroupMount = "/sys/fs/cgroup"

// cgroupsSupported reports whether a cgroup v2 hierarchy is mounted.
func cgroupsSupported() bool {
	_, err := os.Stat(filepath.Join(cgroupMount, cgroupControllers))
	return err == nil
}

// cgroupBaseDir resolves the directory that hosts the service leaves. An empty
// parent uses the supervisor's current cgroup, the container's when it is PID 1.
func cgroupBaseDir(parent string) (string, error) {
	if parent != "" {
		return filepath.Join(cgroupMount, parent), nil
	}
	current, err := currentCgroup()
	if err != nil {
		return "", err
	}
	return filepath.Join(cgroupMount, current), nil
}

// currentCgroup reads the unified cgroup path of the current process.
func currentCgroup() (string, error) {
	file, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return "", errors.Wrap(err, "read current cgroup")
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if path, ok := strings.CutPrefix(scanner.Text(), "0::"); ok {
			return path, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", errors.Wrap(err, "scan current cgroup")
	}
	return "", errors.New("no unified cgroup entry for the current process")
}

// systemMemoryBytes returns the total system memory from /proc/meminfo.
func systemMemoryBytes() (int64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, errors.Wrap(err, "read meminfo")
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kilobytes, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, errors.Wrap(err, "parse total memory")
			}
			return kilobytes * 1024, nil
		}
	}
	return 0, errors.New("no MemTotal entry in meminfo")
}

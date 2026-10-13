package supervisor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
)

// cgroup v2 control files.
const (
	cgroupProcs          = "cgroup.procs"
	cgroupControllers    = "cgroup.controllers"
	cgroupSubtreeControl = "cgroup.subtree_control"
	cgroupCPUWeight      = "cpu.weight"
	cgroupMemoryMax      = "memory.max"
	supervisorLeaf       = "supervisor"
)

// cgroups owns the cgroup v2 leaves that split resources between the services.
// The supervisor and proxies share one leaf; each service runs in its own.
type cgroups struct {
	base   string
	leaves []string
}

// newCgroups moves every process in base into a supervisor leaf, then delegates
// the cpu and memory controllers to sibling leaves once base has no processes.
func newCgroups(base string, pid int) (*cgroups, error) {
	control := &cgroups{base: base}
	if err := control.requireControllers(); err != nil {
		return nil, err
	}
	if err := control.createLeaf(supervisorLeaf); err != nil {
		return nil, err
	}
	if err := control.drain(supervisorLeaf, pid); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(base, cgroupSubtreeControl), "+cpu +memory"); err != nil {
		return nil, errors.Wrap(err, "delegate cpu and memory controllers")
	}
	return control, nil
}

// drain moves the supervisor and any other processes out of base into leaf.
// Moving the supervisor must succeed; other moves are best effort.
func (c *cgroups) drain(leaf string, pid int) error {
	if err := c.place(leaf, pid); err != nil {
		return errors.Wrap(err, "move supervisor into its leaf")
	}
	others, err := readProcs(filepath.Join(c.base, cgroupProcs))
	if err != nil {
		return err
	}
	for _, other := range others {
		if other != pid {
			//nolint:errcheck // best effort: a listed process may have already exited
			_ = c.place(leaf, other)
		}
	}
	return nil
}

// requireControllers checks the base cgroup offers the cpu and memory controllers.
func (c *cgroups) requireControllers() error {
	data, err := os.ReadFile(filepath.Join(c.base, cgroupControllers))
	if err != nil {
		return errors.Wrap(err, "read cgroup controllers")
	}
	available := map[string]bool{}
	for name := range strings.FieldsSeq(string(data)) {
		available[name] = true
	}
	for _, name := range []string{"cpu", "memory"} {
		if !available[name] {
			return errors.Errorf("cgroup %s lacks the %s controller", c.base, name)
		}
	}
	return nil
}

// createLeaf creates a child cgroup and records it for cleanup.
func (c *cgroups) createLeaf(name string) error {
	if err := os.Mkdir(filepath.Join(c.base, name), 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.Wrapf(err, "create cgroup %s", name)
	}
	c.leaves = append(c.leaves, name)
	return nil
}

// limit creates a service leaf and sets its CPU weight and memory cap. A zero
// memory cap is left unset.
func (c *cgroups) limit(name string, cpuWeight uint, memoryMax int64) error {
	if err := c.createLeaf(name); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(c.base, name, cgroupCPUWeight), strconv.FormatUint(uint64(cpuWeight), 10)); err != nil {
		return errors.Wrapf(err, "set %s cpu weight", name)
	}
	if memoryMax > 0 {
		if err := writeFile(filepath.Join(c.base, name, cgroupMemoryMax), strconv.FormatInt(memoryMax, 10)); err != nil {
			return errors.Wrapf(err, "set %s memory max", name)
		}
	}
	return nil
}

// place moves a process into the named leaf.
func (c *cgroups) place(name string, pid int) error {
	return errors.Wrapf(writeFile(filepath.Join(c.base, name, cgroupProcs), strconv.Itoa(pid)), "place pid %d in %s", pid, name)
}

// cleanup removes the service leaves once their processes have exited. The
// supervisor leaf is left in place because the supervisor still runs in it.
func (c *cgroups) cleanup() error {
	removeErrors := make([]error, 0, len(c.leaves))
	for _, name := range c.leaves {
		if name == supervisorLeaf {
			continue
		}
		if err := os.Remove(filepath.Join(c.base, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErrors = append(removeErrors, errors.Wrapf(err, "remove cgroup %s", name))
		}
	}
	return errors.Join(removeErrors...)
}

// writeFile writes a single cgroup control value without a trailing newline.
func writeFile(path string, value string) error {
	return errors.WithStack(os.WriteFile(path, []byte(value), 0o600))
}

// readProcs reads the process identifiers listed in a cgroup.procs file. A
// missing file is treated as an empty cgroup.
func readProcs(path string) ([]int, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Wrap(err, "read cgroup processes")
	}
	var pids []int
	for field := range strings.FieldsSeq(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			return nil, errors.Wrap(err, "parse cgroup process")
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

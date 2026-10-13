package supervisor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestCgroups(t *testing.T) {
	base := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(base, cgroupControllers), []byte("cpuset cpu memory pids\n"), 0o644))

	control, err := newCgroups(base, 4321)
	assert.NoError(t, err)

	assert.Equal(t, "4321", readFile(t, filepath.Join(base, supervisorLeaf, cgroupProcs)))
	assert.Equal(t, "+cpu +memory", readFile(t, filepath.Join(base, cgroupSubtreeControl)))

	assert.NoError(t, control.limit(roleCandidate, 300, 2048))
	assert.Equal(t, "300", readFile(t, filepath.Join(base, roleCandidate, cgroupCPUWeight)))
	assert.Equal(t, "2048", readFile(t, filepath.Join(base, roleCandidate, cgroupMemoryMax)))

	assert.NoError(t, control.limit(roleReference, 100, 0))
	assert.Equal(t, "100", readFile(t, filepath.Join(base, roleReference, cgroupCPUWeight)))
	_, err = os.Stat(filepath.Join(base, roleReference, cgroupMemoryMax))
	assert.True(t, os.IsNotExist(err))

	assert.NoError(t, control.place(roleCandidate, 9999))
	assert.Equal(t, "9999", readFile(t, filepath.Join(base, roleCandidate, cgroupProcs)))
}

func TestCgroupsCleanup(t *testing.T) {
	base := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(base, cgroupControllers), []byte("cpu memory\n"), 0o644))
	control, err := newCgroups(base, 1)
	assert.NoError(t, err)
	// Real cgroupfs rmdir works on leaves with control files; emulate the empty
	// leaves a finished process leaves behind.
	assert.NoError(t, control.createLeaf(roleCandidate))
	assert.NoError(t, control.createLeaf(roleReference))

	assert.NoError(t, control.cleanup())
	_, err = os.Stat(filepath.Join(base, roleCandidate))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(base, supervisorLeaf))
	assert.NoError(t, err)
}

func TestCgroupsRequiresControllers(t *testing.T) {
	base := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(base, cgroupControllers), []byte("cpuset pids\n"), 0o644))
	_, err := newCgroups(base, 1)
	assert.Error(t, err)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	assert.NoError(t, err)
	return string(data)
}

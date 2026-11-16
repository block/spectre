package integration

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
)

// process is a running spectre command, whose output it collects. It is safe
// for concurrent use.
type process struct {
	name   string
	exited chan struct{}
	mu     sync.Mutex
	output bytes.Buffer
	err    error
}

// startProcess runs spectre with args, handing it listeners as descriptors 3
// onwards. It stops when the test ends, logging its output if the test failed.
func startProcess(t *testing.T, name string, listeners []net.Listener, args ...string) *process {
	t.Helper()
	p := &process{name: name, exited: make(chan struct{})}
	cmd := exec.CommandContext(t.Context(), filepath.Join(repositoryRoot(), "dist", "spectre"), append(args, "--log-level=debug")...)
	// Every flag reads a SPECTRE_ variable, which must not leak in from the caller.
	cmd.Env = slices.DeleteFunc(cmd.Environ(), func(variable string) bool { return strings.HasPrefix(variable, "SPECTRE_") })
	cmd.Stdout = p
	cmd.Stderr = p
	cmd.ExtraFiles = files(t, listeners)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 20 * time.Second
	assert.NoError(t, cmd.Start(), "start %s", name)
	for i, file := range cmd.ExtraFiles {
		_ = file.Close()
		_ = listeners[i].Close()
	}
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.err = errors.Wrapf(err, "%s exited", name)
		if err == nil {
			p.err = errors.Errorf("%s exited", name)
		}
		p.mu.Unlock()
		close(p.exited)
	}()
	t.Cleanup(func() {
		<-p.exited
		if t.Failed() {
			t.Logf("%s output:\n%s", name, p)
		}
	})
	return p
}

// files returns a file for each TCP listener, for a process to inherit.
func files(t *testing.T, listeners []net.Listener) []*os.File {
	t.Helper()
	inherited := make([]*os.File, 0, len(listeners))
	for _, listener := range listeners {
		tcp, ok := listener.(*net.TCPListener)
		assert.True(t, ok, "%s is not a TCP listener", listener.Addr())
		file, err := tcp.File()
		assert.NoError(t, err)
		inherited = append(inherited, file)
	}
	return inherited
}

// Write collects the process's output.
func (p *process) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return errors.WithStack2(p.output.Write(data))
}

// failure returns why the process exited, or nil while it runs.
func (p *process) failure() error {
	select {
	case <-p.exited:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	default:
		return nil
	}
}

// String returns everything the process wrote.
func (p *process) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.output.String()
}

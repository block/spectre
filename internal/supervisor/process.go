package supervisor

import (
	"os/exec"

	"github.com/alecthomas/errors"
)

// managedProcess is one child process the supervisor runs and stops.
type managedProcess struct {
	name   string
	cmd    *exec.Cmd
	stdout *serviceLogWriter
}

// newManagedProcess wraps cmd. stdout is the writer passing its output through, so
// a trailing partial line is flushed once it exits.
func newManagedProcess(name string, cmd *exec.Cmd, stdout *serviceLogWriter) *managedProcess {
	return &managedProcess{name: name, cmd: cmd, stdout: stdout}
}

// Name returns the process label used in logs.
func (p *managedProcess) Name() string {
	return p.name
}

// command returns the underlying command, for inspection in tests.
func (p *managedProcess) command() *exec.Cmd {
	return p.cmd
}

// start launches the process.
func (p *managedProcess) start() error {
	return errors.Wrapf(p.cmd.Start(), "start %s", p.name)
}

// pid returns the process identifier, valid once started.
func (p *managedProcess) pid() int {
	return p.cmd.Process.Pid
}

// wait blocks until the process exits and its output has been passed through.
func (p *managedProcess) wait() error {
	err := p.cmd.Wait()
	return errors.Wrapf(errors.Join(err, p.stdout.Flush()), "%s exited", p.name)
}

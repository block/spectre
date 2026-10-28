package supervisor

import (
	"os/exec"
	"syscall"

	"github.com/alecthomas/errors"
)

// managedProcess is one child process the supervisor runs and stops, together
// with everything it starts in its process group.
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

// wait blocks until the process exits and its output has been passed through,
// then kills what is left of its process group so nothing it started outlives it.
func (p *managedProcess) wait() error {
	err := p.cmd.Wait()
	killErr := errors.Wrapf(signalGroup(p.pid(), syscall.SIGKILL), "kill %s process group", p.name)
	return errors.Wrapf(errors.Join(err, killErr, p.stdout.Flush()), "%s exited", p.name)
}

// signalGroup sends sig to every process in the group pgid leads. A group with
// no processes left is not an error.
func signalGroup(pgid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return errors.WithStack(err)
	}
	return nil
}

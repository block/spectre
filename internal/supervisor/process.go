package supervisor

import (
	"os"
	"os/exec"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// managedProcess is one child process the supervisor runs and stops.
type managedProcess struct {
	name   string
	cmd    *exec.Cmd
	stdout *serviceLogWriter
	// afterStart finishes setting up the process once it is running.
	afterStart Option[func() error]
}

// newManagedProcess wraps cmd. stdout is the writer passing its output through, so
// a trailing partial line is flushed once it exits.
func newManagedProcess(name string, cmd *exec.Cmd, stdout *serviceLogWriter) *managedProcess {
	return &managedProcess{name: name, cmd: cmd, stdout: stdout, afterStart: None[func() error]()}
}

// Name returns the process label used in logs.
func (p *managedProcess) Name() string {
	return p.name
}

// command returns the underlying command, for inspection in tests.
func (p *managedProcess) command() *exec.Cmd {
	return p.cmd
}

// handOff passes file to the process as its first extra descriptor, and runs
// afterStart once the process is running.
func (p *managedProcess) handOff(file *os.File, afterStart func() error) { //nolint:unused // Only the Linux egress sandbox hands off a descriptor.
	p.cmd.ExtraFiles = []*os.File{file}
	p.afterStart = Some(afterStart)
}

// start launches the process.
func (p *managedProcess) start() error {
	if err := p.cmd.Start(); err != nil {
		return errors.Wrapf(err, "start %s", p.name)
	}
	if afterStart, ok := p.afterStart.Get(); ok {
		return errors.Wrapf(afterStart(), "set up %s", p.name)
	}
	return nil
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

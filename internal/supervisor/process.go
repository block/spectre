package supervisor

import (
	"os/exec"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/logger"
)

// managedProcess is one child process the supervisor runs and stops.
type managedProcess struct {
	name   string
	cmd    *exec.Cmd
	stdout *logger.LineWriter
}

// newManagedProcess wraps cmd. stdout is the writer logging its output, so a
// trailing partial line is flushed once it exits.
func newManagedProcess(name string, cmd *exec.Cmd, stdout *logger.LineWriter) *managedProcess {
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

// wait blocks until the process exits and its output has been logged.
func (p *managedProcess) wait() error {
	err := p.cmd.Wait()
	p.stdout.Flush()
	return errors.Wrapf(err, "%s exited", p.name)
}

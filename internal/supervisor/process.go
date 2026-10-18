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
	output []*logger.LineWriter
}

// newManagedProcess wraps cmd. output lists the writers that log its streams, so
// their trailing partial lines are flushed once it exits.
func newManagedProcess(name string, cmd *exec.Cmd, output ...*logger.LineWriter) *managedProcess {
	return &managedProcess{name: name, cmd: cmd, output: output}
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
	for _, writer := range p.output {
		writer.Flush()
	}
	return errors.Wrapf(err, "%s exited", p.name)
}

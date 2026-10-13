package supervisor

import (
	"bytes"
	"io"
	"os/exec"
	"sync"

	"github.com/alecthomas/errors"
)

// managedProcess is one child process the supervisor runs and stops.
type managedProcess struct {
	name string
	cmd  *exec.Cmd
}

// newManagedProcess wraps cmd, tagging its output with name.
func newManagedProcess(name string, cmd *exec.Cmd, output io.Writer) *managedProcess {
	cmd.Stdout = newPrefixWriter(output, name)
	cmd.Stderr = newPrefixWriter(output, name)
	return &managedProcess{name: name, cmd: cmd}
}

// Name returns the process label used in logs and output prefixes.
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

// wait blocks until the process exits.
func (p *managedProcess) wait() error {
	return errors.Wrapf(p.cmd.Wait(), "%s exited", p.name)
}

// prefixWriter tags each line written to it with a process name before passing
// it to the underlying writer, buffering partial lines between writes.
type prefixWriter struct {
	prefix []byte
	out    io.Writer
	mu     sync.Mutex
	line   []byte
}

// newPrefixWriter returns a writer that prefixes each line with name.
func newPrefixWriter(out io.Writer, name string) *prefixWriter {
	return &prefixWriter{prefix: []byte(name + " | "), out: out}
}

// Write emits complete lines with the prefix and holds any trailing partial line.
func (w *prefixWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.line = append(w.line, data...)
	for {
		index := bytes.IndexByte(w.line, '\n')
		if index < 0 {
			return len(data), nil
		}
		if _, err := w.out.Write(w.prefix); err != nil {
			return len(data), errors.WithStack(err)
		}
		if _, err := w.out.Write(w.line[:index+1]); err != nil {
			return len(data), errors.WithStack(err)
		}
		w.line = w.line[index+1:]
	}
}

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

// syncWriter serialises writes from the proxies and services that share one
// output, so their complete lines never interleave.
type syncWriter struct {
	mu  sync.Mutex
	out io.Writer
}

// newSyncWriter returns a writer that serialises concurrent writes to out.
func newSyncWriter(out io.Writer) *syncWriter {
	return &syncWriter{out: out}
}

// Write passes data to the underlying writer under the shared lock.
func (w *syncWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.out.Write(data)
	return n, errors.WithStack(err)
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

// Write emits each complete line as one prefixed write and holds any trailing
// partial line.
func (w *prefixWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.line = append(w.line, data...)
	for {
		index := bytes.IndexByte(w.line, '\n')
		if index < 0 {
			return len(data), nil
		}
		row := append(append([]byte(nil), w.prefix...), w.line[:index+1]...)
		if _, err := w.out.Write(row); err != nil {
			return len(data), errors.WithStack(err)
		}
		w.line = w.line[index+1:]
	}
}

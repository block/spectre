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
func newManagedProcess(name string, cmd *exec.Cmd, output io.Writer, json bool) *managedProcess {
	cmd.Stdout = newTagWriter(output, name, json)
	cmd.Stderr = newTagWriter(output, name, json)
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

// tagWriter tags each line with its process name before writing it on, buffering
// partial lines: a "name | " prefix for text, a "process" field for JSON.
type tagWriter struct {
	prefix  []byte
	jsonTag []byte
	json    bool
	out     io.Writer
	mu      sync.Mutex
	line    []byte
}

// newTagWriter returns a writer that tags each line with name, as a text prefix
// or a JSON "process" field depending on json.
func newTagWriter(out io.Writer, name string, json bool) *tagWriter {
	// Process names are fixed identifiers, so they need no JSON escaping.
	return &tagWriter{
		prefix:  []byte(name + " | "),
		jsonTag: []byte(`"process":"` + name + `"`),
		json:    json,
		out:     out,
	}
}

// Write emits each complete line as one tagged write and holds any trailing
// partial line.
func (w *tagWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.line = append(w.line, data...)
	for {
		index := bytes.IndexByte(w.line, '\n')
		if index < 0 {
			return len(data), nil
		}
		if _, err := w.out.Write(w.tag(w.line[:index+1])); err != nil {
			return len(data), errors.WithStack(err)
		}
		w.line = w.line[index+1:]
	}
}

// tag returns line with its process name attached: a "name | " prefix for text,
// or a "process" field for a JSON object. Other lines pass through unchanged.
func (w *tagWriter) tag(line []byte) []byte {
	if !w.json {
		return append(append([]byte(nil), w.prefix...), line...)
	}
	trimmed := bytes.TrimLeft(line, " \t")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return line
	}
	brace := len(line) - len(trimmed)
	rest := line[brace+1:]
	// Omit the separator for an empty object so "{}" stays valid JSON.
	separator := []byte(",")
	if next := bytes.TrimLeft(rest, " \t\r\n"); len(next) > 0 && next[0] == '}' {
		separator = nil
	}
	row := make([]byte, 0, len(line)+len(w.jsonTag)+len(separator))
	row = append(row, line[:brace+1]...)
	row = append(row, w.jsonTag...)
	row = append(row, separator...)
	return append(row, rest...)
}

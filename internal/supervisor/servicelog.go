package supervisor

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"

	"github.com/alecthomas/errors"
)

// maxServiceLine bounds the output held while waiting for a newline, so a service
// writing without newlines cannot exhaust the supervisor's memory.
const maxServiceLine = 64 * 1024

// serviceLogWriter passes a service's stdout through line by line, labelling each
// line with the service's role. It never decodes or rewrites the service's own fields.
type serviceLogWriter struct {
	output  io.Writer
	role    string
	label   []byte
	pending []byte
}

// newServiceLogWriter writes labelled lines to output, one Write per line, so an
// output that serialises Writes, as *os.File does, never interleaves two services.
func newServiceLogWriter(output io.Writer, role string) *serviceLogWriter {
	// Both strings are ASCII constants, so Go quoting is valid JSON.
	label := []byte(strconv.Quote(subsystemKey) + ":" + strconv.Quote(role))
	return &serviceLogWriter{output: output, role: role, label: label}
}

// Write passes every complete line in data through and holds back the remainder.
// Only data is scanned, so a long unterminated stream costs linear work.
func (w *serviceLogWriter) Write(data []byte) (n int, err error) {
	remaining := data
	for len(remaining) > 0 {
		end := bytes.IndexByte(remaining, '\n')
		if end < 0 {
			if err := w.hold(remaining); err != nil {
				return len(data) - len(remaining), err
			}
			break
		}
		if err := w.hold(remaining[:end]); err != nil {
			return len(data) - len(remaining), err
		}
		if err := w.emit(); err != nil {
			return len(data) - len(remaining), err
		}
		remaining = remaining[end+1:]
	}
	return len(data), nil
}

// Flush passes through trailing output that has no newline. Call it once the
// service's stdout has closed.
func (w *serviceLogWriter) Flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	return w.emit()
}

// hold buffers fragment until its line completes, passing it through in
// maxServiceLine pieces when a service writes that much without a newline.
func (w *serviceLogWriter) hold(fragment []byte) error {
	for len(fragment) > 0 {
		room := min(maxServiceLine-len(w.pending), len(fragment))
		w.pending = append(w.pending, fragment[:room]...)
		fragment = fragment[room:]
		if len(w.pending) == maxServiceLine {
			if err := w.emit(); err != nil {
				return err
			}
		}
	}
	return nil
}

// emit writes the pending line with its label and clears it.
func (w *serviceLogWriter) emit() error {
	line := w.annotate(make([]byte, 0, len(w.pending)+len(w.label)+2), w.pending)
	w.pending = w.pending[:0]
	_, err := w.output.Write(append(line, '\n'))
	return errors.WithStack(err)
}

// annotate inserts the label as the first member of a JSON object line, keeping
// every other byte, and prefixes any other non-blank line with the role.
func (w *serviceLogWriter) annotate(out []byte, line []byte) []byte {
	trimmed := bytes.TrimLeft(line, " \t\r")
	if len(trimmed) == 0 {
		return append(out, line...)
	}
	if trimmed[0] != '{' || !json.Valid(line) {
		out = append(out, w.role...)
		out = append(out, ": "...)
		return append(out, line...)
	}
	open := len(line) - len(trimmed) + 1
	out = append(out, line[:open]...)
	out = append(out, w.label...)
	if body := bytes.TrimLeft(line[open:], " \t\r"); body[0] != '}' {
		out = append(out, ',')
	}
	return append(out, line[open:]...)
}

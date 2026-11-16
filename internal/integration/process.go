package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// Fields are the attributes of a JSON log record.
type Fields map[string]any

// matches reports whether record has every one of the fields.
func (f Fields) matches(record Fields) bool {
	for key, value := range f {
		if record[key] != value {
			return false
		}
	}
	return true
}

// process is a running spectre command, whose output it collects. It is safe
// for concurrent use.
type process struct {
	name    string
	exited  chan struct{}
	mu      sync.Mutex
	partial []byte
	lines   []string
	records []Fields
	err     error
}

// startProcess runs the spectre binary with args and JSON debug logs. It stops
// when the test ends, logging its output if the test failed.
func startProcess(t *testing.T, name string, args ...string) *process {
	t.Helper()
	p := &process{name: name, exited: make(chan struct{})}
	args = append(args, "--log-level=debug", "--log-json=DEFAULT")
	cmd := exec.CommandContext(t.Context(), filepath.Join(repositoryRoot(), "dist", "spectre"), args...)
	// Every flag reads a SPECTRE_ variable, which must not leak in from the caller.
	cmd.Env = slices.DeleteFunc(cmd.Environ(), func(variable string) bool { return strings.HasPrefix(variable, "SPECTRE_") })
	cmd.Stdout = p
	cmd.Stderr = p
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 20 * time.Second
	assert.NoError(t, cmd.Start(), "start %s", name)
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

// Write collects each complete line, keeping any partial line for the next write.
func (p *process) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.partial = append(p.partial, data...)
	for {
		end := bytes.IndexByte(p.partial, '\n')
		if end < 0 {
			return len(data), nil
		}
		line := string(p.partial[:end])
		p.partial = p.partial[end+1:]
		p.lines = append(p.lines, line)
		var record Fields
		if json.Unmarshal([]byte(line), &record) == nil {
			p.records = append(p.records, record)
		}
	}
}

// find returns the first record with every one of fields.
func (p *process) find(fields Fields) (Fields, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, record := range p.records {
		if fields.matches(record) {
			return record, true
		}
	}
	return nil, false
}

// count returns how many records have every one of fields.
func (p *process) count(fields Fields) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	matched := 0
	for _, record := range p.records {
		if fields.matches(record) {
			matched++
		}
	}
	return matched
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

// describe names the process's records with fields, for test failures.
func (p *process) describe(fields Fields) string {
	return fmt.Sprintf("a %s record matching %v", p.name, fields)
}

// String returns everything the process wrote.
func (p *process) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.lines, "\n") + "\n" + string(p.partial)
}

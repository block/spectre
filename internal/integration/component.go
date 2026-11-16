package integration

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/logger"
)

// component is the server or the proxy, running in a goroutine of the test.
type component struct {
	name string
	done chan struct{}
	err  error
}

func newComponent(name string) *component {
	return &component{name: name, done: make(chan struct{})}
}

// run runs the component until it returns.
func (c *component) run(ctx context.Context, run func(ctx context.Context) error) {
	defer close(c.done)
	c.err = run(ctx)
}

// wait waits for the component to stop.
func (c *component) wait() {
	<-c.done
}

// failure returns why the component stopped, or nil while it runs.
func (c *component) failure() error {
	select {
	case <-c.done:
		if c.err != nil {
			return errors.Wrapf(c.err, "%s stopped", c.name)
		}
		return errors.Errorf("%s stopped", c.name)
	default:
		return nil
	}
}

// withLogs returns the test's context with a debug logger, whose output the
// test logs if it fails. The output outlives the test, as the server's requests may.
func withLogs(t *testing.T) context.Context {
	t.Helper()
	output := newLogs()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("mirror logs:\n%s", output)
		}
	})
	config := logger.NewConfig()
	config.Level = slog.LevelDebug
	log := logger.New(config, output) //nolint:forbidigo // A test library builds the logger of the mirror it runs, as a test does.
	return logger.WithLogger(t.Context(), log)
}

// logs collects log output. It is safe for concurrent use.
type logs struct {
	mu     sync.Mutex
	output bytes.Buffer
}

func newLogs() *logs {
	return &logs{}
}

func (l *logs) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return errors.WithStack2(l.output.Write(data))
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.output.String()
}

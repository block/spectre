//go:build integration

package integration_test

import (
	"context"

	"github.com/alecthomas/errors"
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

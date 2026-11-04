package stream_test

import (
	"context"
	"io"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/stream"
)

// conn records what a channel sends on one stream.
type conn struct {
	mu   sync.Mutex
	sent []string
	// Sends fail with io.EOF once this many messages are sent, unless it is zero.
	limit  int
	closed bool
}

func (c *conn) Send(message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.limit > 0 && len(c.sent) >= c.limit {
		return io.EOF
	}
	c.sent = append(c.sent, message)
	return nil
}

func (c *conn) Keepalive() error {
	return c.Send("keepalive")
}

func (c *conn) CloseAndReceive() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.limit > 0 {
		return errors.New("server ended the stream")
	}
	return nil
}

// sent is what a conn was sent and whether it was closed.
type sent struct {
	Messages []string
	Closed   bool
}

// dialer hands out a new conn for every stream a channel opens.
type dialer struct {
	mu    sync.Mutex
	conns []*conn
	limit []int
}

func (d *dialer) dial(context.Context) (stream.Conn[string], error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	opened := &conn{}
	if len(d.conns) < len(d.limit) {
		opened.limit = d.limit[len(d.conns)]
	}
	d.conns = append(d.conns, opened)
	return opened, nil
}

func (d *dialer) streams() []sent {
	d.mu.Lock()
	defer d.mu.Unlock()
	streams := make([]sent, 0, len(d.conns))
	for _, opened := range d.conns {
		opened.mu.Lock()
		streams = append(streams, sent{Messages: append([]string(nil), opened.sent...), Closed: opened.closed})
		opened.mu.Unlock()
	}
	return streams
}

// run runs a channel until the test ends.
func run(t *testing.T, config stream.Config, d *dialer) *stream.OutChannel[string] {
	t.Helper()
	channel := stream.NewOutChannel(config, d.dial, func() []string { return []string{"state"} })
	ctx, cancel := context.WithCancel(t.Context())
	go channel.Run(ctx)
	t.Cleanup(func() {
		cancel()
		synctest.Wait()
	})
	return channel
}

var config = stream.Config{KeepaliveInterval: 30 * time.Second, MaxAge: time.Hour}

func TestOutChannelResyncsThenSendsAndKeepsAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{}
		channel := run(t, config, d)
		assert.True(t, channel.Send("event"))
		synctest.Wait()
		assert.Equal(t, []sent{{Messages: []string{"state", "event"}}}, d.streams())

		// Only a quiet stream sends keepalives.
		time.Sleep(20 * time.Second)
		assert.True(t, channel.Send("later"))
		time.Sleep(20 * time.Second)
		synctest.Wait()
		assert.Equal(t, []sent{{Messages: []string{"state", "event", "later"}}}, d.streams())
		time.Sleep(10 * time.Second)
		synctest.Wait()
		assert.Equal(t, []sent{{Messages: []string{"state", "event", "later", "keepalive"}}}, d.streams())
	})
}

func TestOutChannelReconnectsWithBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The first two streams end after the resync, the third lasts.
		d := &dialer{limit: []int{1, 1}}
		channel := run(t, config, d)
		synctest.Wait()
		assert.True(t, channel.Send("lost"))
		synctest.Wait()
		failed := sent{Messages: []string{"state"}, Closed: true}
		assert.Equal(t, []sent{failed}, d.streams())

		// Retries wait between half and all of a delay that doubles from 1s.
		time.Sleep(time.Second)
		synctest.Wait()
		assert.Equal(t, []sent{failed, {Messages: []string{"state"}}}, d.streams())
		assert.True(t, channel.Send("lost again"))
		time.Sleep(time.Second - time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, 2, len(d.streams()))
		time.Sleep(time.Second)
		synctest.Wait()
		assert.Equal(t, []sent{failed, failed, {Messages: []string{"state"}}}, d.streams())
	})
}

func TestOutChannelReplacesAgedStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{}
		run(t, stream.Config{KeepaliveInterval: time.Hour, MaxAge: 10 * time.Minute}, d)
		synctest.Wait()
		time.Sleep(5*time.Minute - time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, []sent{{Messages: []string{"state"}}}, d.streams())

		// A stream lasts between half and all of its age, and is replaced at once.
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		assert.Equal(t, []sent{{Messages: []string{"state"}, Closed: true}, {Messages: []string{"state"}}}, d.streams())
	})
}

func TestOutChannelDropsWhenQueueIsFull(t *testing.T) {
	channel := stream.NewOutChannel(config, (&dialer{}).dial, func() []string { return nil })
	queued := 0
	for channel.Send("event") {
		queued++
	}
	assert.Equal(t, 256, queued)
}

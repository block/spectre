// Package stream keeps long-lived client streams open. An Outbox keeps one
// stream from a client open through failures and sends it what producers queue.
// Every stream it opens begins with a resync of the client's whole state, so
// only events need queueing, and a stream may fail at any time without losing
// state. EndEarly lets a server end such a stream before the client does.
package stream

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/logger"
)

// Conn is one open client stream.
type Conn[M any] interface {
	// Send sends message. It fails once the server has ended the stream.
	Send(message M) error
	// CloseAndReceive ends the stream and returns the error the server ended it with.
	CloseAndReceive() error
}

// Config configures how an outbox keeps its stream open.
type Config struct {
	KeepaliveInterval time.Duration `default:"30s" help:"How often to send a keepalive on a stream that is otherwise quiet."`
	MaxAge            time.Duration `name:"stream-max-age" default:"10m" help:"How long a stream lasts, with jitter, before it is replaced, so streams spread across servers."`
}

// Validate checks that the intervals are positive.
func (c Config) Validate() error {
	if c.KeepaliveInterval <= 0 || c.MaxAge <= 0 {
		return errors.New("stream intervals must be positive")
	}
	return nil
}

// Retries of a failed stream back off between these delays.
const (
	minRetryDelay = time.Second
	maxRetryDelay = time.Minute
)

// queueSize is how many messages wait for a stream before Send drops them.
const queueSize = 256

// Outbox keeps one stream open and sends it the messages producers queue.
type Outbox[M any] struct {
	config    Config
	dial      func(ctx context.Context) (Conn[M], error)
	resync    func() []M
	keepalive func() M
	queue     chan M
}

// NewOutbox returns an outbox that opens streams with dial. Each stream begins
// with the messages resync returns, and a quiet stream sends keepalive.
func NewOutbox[M any](config Config, dial func(ctx context.Context) (Conn[M], error), resync func() []M, keepalive func() M) *Outbox[M] {
	return &Outbox[M]{config: config, dial: dial, resync: resync, keepalive: keepalive, queue: make(chan M, queueSize)}
}

// Send queues message for the stream without blocking. It drops the message
// when the queue is full. A queued message is lost if its stream fails.
func (o *Outbox[M]) Send(message M) (queued bool) {
	select {
	case o.queue <- message:
		return true
	default:
		return false
	}
}

// Run keeps a stream open until ctx ends, replacing it when it fails, with
// backoff, or when it reaches its age. Failures are only logged.
func (o *Outbox[M]) Run(ctx context.Context) {
	delay := minRetryDelay
	for {
		opened := time.Now()
		err := o.stream(ctx)
		if ctx.Err() != nil {
			return
		}
		// Only consecutive quick failures back off further.
		if err == nil || time.Since(opened) > maxRetryDelay {
			delay = minRetryDelay
		}
		if err == nil {
			continue
		}
		wait := jitter(delay)
		logger.FromContext(ctx).WarnContext(ctx, "Lost a stream to the server", "error", err, "retry", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

// stream sends on one stream until it fails or ctx ends. It returns nil once
// the stream reaches its age and the server accepts it closing.
func (o *Outbox[M]) stream(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, err := o.dial(ctx)
	if err != nil {
		return errors.Wrap(err, "open stream")
	}
	for _, message := range o.resync() {
		if err := conn.Send(message); err != nil {
			return failed(conn, err)
		}
	}
	keepalive := time.NewTimer(o.config.KeepaliveInterval)
	defer keepalive.Stop()
	age := time.NewTimer(jitter(o.config.MaxAge))
	defer age.Stop()
	for {
		var message M
		select {
		case <-ctx.Done():
			cancel()
			_ = conn.CloseAndReceive() //nolint:errcheck // Cancelling has already aborted the stream.
			return errors.WithStack(ctx.Err())
		case <-age.C:
			return errors.Wrap(conn.CloseAndReceive(), "close aged stream")
		case <-keepalive.C:
			message = o.keepalive()
		case message = <-o.queue:
		}
		if err := conn.Send(message); err != nil {
			return failed(conn, err)
		}
		keepalive.Reset(o.config.KeepaliveInterval)
	}
}

// failed closes a stream whose send failed. A send fails once the server has
// ended the stream, and closing returns why it did.
func failed[M any](conn Conn[M], err error) error {
	if closeErr := conn.CloseAndReceive(); closeErr != nil {
		return errors.Wrap(closeErr, "stream ended")
	}
	return errors.Wrap(err, "send on stream")
}

// jitter returns a random duration between half of d and d.
func jitter(d time.Duration) time.Duration {
	return d/2 + rand.N(d/2+1)
}

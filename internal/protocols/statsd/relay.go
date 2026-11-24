package statsd

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
)

const (
	// maxDatagramBytes is the largest datagram read whole. StatsD clients send far smaller ones.
	maxDatagramBytes = 65535
	// queuedDatagrams bounds the datagrams waiting for the agent. Beyond it they are dropped.
	queuedDatagrams = 1024
	// writeTimeout bounds one write to an agent that has stopped reading.
	writeTimeout = 100 * time.Millisecond
)

// Forward sends every datagram on conn to destination unchanged until ctx ends,
// then closes conn. A datagram the agent does not take is dropped, as StatsD clients do.
func Forward(ctx context.Context, conn net.PacketConn, destination *netaddr.PacketEndpoint) error {
	forwarder := newForwarder(ctx, destination)
	queue := make(chan []byte, queuedDatagrams)
	written := make(chan struct{})
	go func() {
		defer close(written)
		forwarder.run(ctx, queue)
	}()
	overflowing := false
	err := receive(ctx, conn, func(datagram []byte) {
		select {
		case queue <- bytes.Clone(datagram):
			overflowing = false
		default:
			if !overflowing {
				overflowing = true
				forwarder.warnOverflow(ctx)
			}
		}
	})
	close(queue)
	<-written
	return err
}

// Discard reads and drops every datagram on conn until ctx ends, then closes conn.
func Discard(ctx context.Context, conn net.PacketConn) error {
	discarded := false
	return receive(ctx, conn, func([]byte) {
		if !discarded {
			discarded = true
			logger.FromContext(ctx).InfoContext(ctx, "Discarding candidate StatsD metrics")
		}
	})
}

// receive passes each datagram on conn to handle until ctx ends or a read fails.
// The datagram is only valid during the call.
func receive(ctx context.Context, conn net.PacketConn, handle func(datagram []byte)) error {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer conn.Close() //nolint:errcheck // Closed once ctx ends, or after a failed read.
	buffer := make([]byte, maxDatagramBytes)
	for {
		size, _, err := conn.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.Wrap(err, "read StatsD datagram")
		}
		handle(buffer[:size])
	}
}

// forwarder sends datagrams to the agent, dialling again after a failure so the
// agent may start late or restart.
type forwarder struct {
	destination *netaddr.PacketEndpoint
	log         *slog.Logger
	conn        Option[net.Conn]
	forwarded   bool
	failing     bool
}

// newForwarder returns a forwarder to destination that has not dialled it yet.
func newForwarder(ctx context.Context, destination *netaddr.PacketEndpoint) *forwarder {
	log := logger.FromContext(ctx).With("forward", destination.Address())
	return &forwarder{destination: destination, log: log, conn: None[net.Conn]()}
}

// run sends each datagram from queue until it closes or ctx ends, then hangs up.
func (f *forwarder) run(ctx context.Context, queue <-chan []byte) {
	defer f.close()
	for {
		select {
		case <-ctx.Done():
			return
		case datagram, ok := <-queue:
			if !ok {
				return
			}
			f.send(ctx, datagram)
		}
	}
}

// warnOverflow logs that datagrams are dropped because the queue is full. It only reads the logger,
// so the receiving goroutine may call it.
func (f *forwarder) warnOverflow(ctx context.Context) {
	f.log.WarnContext(ctx, "Dropping reference StatsD metrics while the agent catches up")
}

// send forwards datagram, logging only when forwarding starts, fails, or recovers.
func (f *forwarder) send(ctx context.Context, datagram []byte) {
	if err := f.write(ctx, datagram); err != nil {
		f.close()
		if !f.failing {
			f.failing = true
			f.log.WarnContext(ctx, "Dropping reference StatsD metrics until the agent takes them", "error", err)
		}
		return
	}
	switch {
	case f.failing:
		f.failing = false
		f.log.InfoContext(ctx, "Forwarding reference StatsD metrics again")
	case !f.forwarded:
		f.log.InfoContext(ctx, "Forwarding reference StatsD metrics")
	}
	f.forwarded = true
}

// write dials the agent unless connected, then sends datagram within writeTimeout.
func (f *forwarder) write(ctx context.Context, datagram []byte) error {
	conn, ok := f.conn.Get()
	if !ok {
		dialled, err := f.destination.Dial(ctx)
		if err != nil {
			return errors.WithStack(err)
		}
		conn = dialled
		f.conn = Some(conn)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return errors.Wrap(err, "bound StatsD write")
	}
	_, err := conn.Write(datagram)
	return errors.Wrap(err, "forward StatsD datagram")
}

// close drops the connection to the agent, if any.
func (f *forwarder) close() {
	if conn, ok := f.conn.Get(); ok {
		_ = conn.Close()
		f.conn = None[net.Conn]()
	}
}

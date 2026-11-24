package statsd

import (
	"context"
	"log/slog"
	"net"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
)

// maxDatagramBytes is the largest datagram read whole. StatsD clients send far smaller ones.
const maxDatagramBytes = 65535

// Forward sends every datagram on conn to destination unchanged until ctx ends,
// then closes conn. A datagram the agent does not take is dropped, as StatsD clients do.
func Forward(ctx context.Context, conn net.PacketConn, destination *netaddr.PacketEndpoint) error {
	forwarder := newForwarder(ctx, destination)
	defer forwarder.close()
	return receive(ctx, conn, func(datagram []byte) { forwarder.send(ctx, datagram) })
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

// send forwards datagram, logging only when forwarding starts, fails, or recovers.
func (f *forwarder) send(ctx context.Context, datagram []byte) {
	log := f.log
	if err := f.write(ctx, datagram); err != nil {
		f.close()
		if !f.failing {
			f.failing = true
			log.WarnContext(ctx, "Dropping reference StatsD metrics until the agent takes them", "error", err)
		}
		return
	}
	switch {
	case f.failing:
		f.failing = false
		log.InfoContext(ctx, "Forwarding reference StatsD metrics again")
	case !f.forwarded:
		log.InfoContext(ctx, "Forwarding reference StatsD metrics")
	}
	f.forwarded = true
}

// write dials the agent unless connected, then sends datagram.
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

package mysqlserver

import (
	"context"
	"sync"

	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

// observer follows a reference connection as the relay passes it, and reads the reply
// of each text statement and database selection. It never changes what is relayed.
type observer struct {
	ctx    context.Context //nolint:containedctx // The relay's goroutines share the connection's context.
	server *Server
	// capability is what the reference and the database agreed on, which shapes replies.
	capability uint32
	mu         sync.Mutex
	session    normaliser.Session
	// reading is the reply of command, if it is being observed.
	reading Option[*replyReader]
	command normaliser.Command
	framer  *framer
	// held is what the reply being read holds of the saved replies budget.
	held int
}

func newObserver(ctx context.Context, server *Server, session normaliser.Session, capability uint32) *observer {
	return &observer{ctx: ctx, server: server, capability: capability, session: session, framer: newFramer()}
}

// sent starts a command, whose reply is everything the database sends until the next.
// A command that is not whole is never observed.
func (o *observer) sent(payload []byte, whole bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stop()
	if !whole || len(payload) == 0 || !o.server.offering() {
		return
	}
	switch payload[0] {
	case mysql.COM_QUERY:
		o.reading, o.command = Some(newReplyReader(o.capability)), normaliser.Query{SQL: string(payload[1:])}
	case mysql.COM_INIT_DB:
		o.reading, o.command = Some(newReplyReader(o.capability)), normaliser.UseDatabase{Name: string(payload[1:])}
	}
}

// received reads data the database sent, before the relay passes it on. Its result
// hands a finished reply to the server once the reference has it too.
func (o *observer) received(data []byte) (deliver func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	reader, reading := o.reading.Get()
	if !reading {
		return func() {}
	}
	o.framer.write(data)
	for {
		payload, framed := o.framer.next()
		reply := None[normaliser.Reply]()
		if framed {
			var err error
			if reply, err = reader.read(payload); err != nil {
				o.stop()
				logger.FromContext(o.ctx).WarnContext(o.ctx, "MySQL reply is not offered", "event", logger.EventCorrelation, "reason", err)
				return func() {}
			}
			if !reader.offerable() {
				o.stop()
				return func() {}
			}
		}
		if !o.hold(reader.size() + o.framer.pending()) {
			o.stop()
			return o.units(o.command, normaliser.Rows{}, o.server.exceed)
		}
		finished, ok := reply.Get()
		if !framed {
			return func() {}
		}
		if !ok {
			continue
		}
		// The saved reply takes over what the reply held once it is offered.
		held := o.held
		o.held = 0
		o.stop()
		return o.units(o.command, finished, func(ctx context.Context, units []normaliser.Unit) {
			o.server.release(held)
			o.server.offer(ctx, units)
		})
	}
}

// hold keeps size bytes of the budget for the reply being read, and reports whether
// they fit.
func (o *observer) hold(size int) (held bool) {
	switch {
	case size > o.held && !o.server.reserve(size-o.held):
		return false
	case size < o.held:
		o.server.release(o.held - size)
	}
	o.held = size
	return true
}

// close stops observing when the connection ends.
func (o *observer) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stop()
}

// stop ignores the rest of the command's reply, and returns what it held.
func (o *observer) stop() {
	o.server.release(o.held)
	o.reading, o.framer, o.held = None[*replyReader](), newFramer(), 0
}

// units turns a finished command into units now, so the session sees commands in
// order, and hands them to use later.
func (o *observer) units(command normaliser.Command, reply normaliser.Reply,
	use func(ctx context.Context, units []normaliser.Unit),
) (deliver func()) {
	units, err := o.session.Reference(o.ctx, command, reply)
	if err != nil {
		logger.FromContext(o.ctx).WarnContext(o.ctx, "Reference command is not offered", "event", logger.EventCorrelation, "reason", err)
		return func() {}
	}
	return func() { use(o.ctx, units) }
}

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
	// transactions is only used by the connection's offers, which run in order, so it needs no lock.
	transactions *referenceTransactions
	// capability is what the reference and the database agreed on, which shapes replies.
	capability uint32
	mu         sync.Mutex
	session    normaliser.Session
	// reading is the reply of the command sent, if it is being observed.
	reading Option[*replyReader]
	pending sentCommand
	framer  *framer
	// held is what the reply being read holds of the saved replies budget.
	held int
}

func newObserver(ctx context.Context, server *Server, session normaliser.Session, capability uint32) *observer {
	return &observer{
		ctx: ctx, server: server, capability: capability, session: session, framer: newFramer(),
		transactions: newReferenceTransactions(),
	}
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
	if payload[0] == mysql.COM_QUERY || payload[0] == mysql.COM_INIT_DB {
		o.reading, o.pending = Some(newReplyReader(o.capability)), sentCommand{code: payload[0], text: string(payload[1:])}
	}
}

// received reads data the database sent, before the relay passes it on. When the data
// finishes a reply, it returns the offer to run once the reference has it too.
func (o *observer) received(data []byte) (offer Option[func()]) {
	o.mu.Lock()
	defer o.mu.Unlock()
	reader, reading := o.reading.Get()
	if !reading {
		return None[func()]()
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
				return Some(o.follow(o.pending))
			}
			if !reader.offerable() {
				o.stop()
				return Some(o.follow(o.pending))
			}
		}
		if !o.hold(reader.size() + o.framer.pending()) {
			o.stop()
			return Some(o.units(o.pending, normaliser.Rows{}, func(ctx context.Context, units []normaliser.Unit, _ Option[transactionID]) {
				o.server.exceed(ctx, units)
			}))
		}
		finished, ok := reply.Get()
		if !framed {
			return None[func()]()
		}
		if !ok {
			continue
		}
		// The saved reply takes over what the reply held once it is offered.
		held := o.held
		o.held = 0
		o.stop()
		return Some(o.units(o.pending, finished, func(ctx context.Context, units []normaliser.Unit, names Option[transactionID]) {
			o.server.release(held)
			o.server.offer(ctx, units, names)
		}))
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

// units returns what turns a finished command into units, and hands them to use.
// The relay queues it once the reference has the reply, so normalising never delays it.
func (o *observer) units(sent sentCommand, reply normaliser.Reply,
	use func(ctx context.Context, units []normaliser.Unit, names Option[transactionID]),
) (deliver func()) {
	return func() {
		// A connection's offers run in order on one goroutine, so the session sees commands in order.
		command := o.server.command(sent.code, sent.text)
		units, names := o.transactions.scope(command, func() []normaliser.Unit {
			units, err := o.session.Reference(o.ctx, command, reply)
			if err != nil {
				logger.FromContext(o.ctx).WarnContext(o.ctx, "Reference command is not offered", "event", logger.EventCorrelation, "reason", err)
				// use still gets no units, so the reply's hold on the budget is returned.
				return nil
			}
			return units
		})
		use(o.ctx, units, names)
	}
}

// follow returns what follows a command whose reply is not offered through the connection's transactions.
func (o *observer) follow(sent sentCommand) (deliver func()) {
	return func() {
		o.transactions.follow(o.server.command(sent.code, sent.text))
	}
}

// sentCommand is a command the reference sent, before it is parsed.
type sentCommand struct {
	code byte
	text string
}

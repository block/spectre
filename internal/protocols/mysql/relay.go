package mysql

import (
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"regexp"
	"sync"

	"github.com/alecthomas/errors"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/pingcap/tidb/pkg/parser/ast"

	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/protocols/mysql/normaliser"
)

// firstThread is the first thread ID the proxy gives references, far above the
// database's own, so a KILL names one or the other.
const firstThread uint32 = 3_000_000_000

// threads maps the thread IDs the proxy gave references to their database threads.
type threads struct {
	mu       sync.Mutex
	next     uint32
	database map[uint32]uint32
	// mentionsKill finds statements worth parsing as a KILL, so the relay parses few statements.
	mentionsKill *regexp.Regexp
	parser       *normaliser.Parser
}

func newThreads(parser *normaliser.Parser) *threads {
	return &threads{
		next:         firstThread,
		database:     map[uint32]uint32{},
		mentionsKill: regexp.MustCompile(`(?i)kill`),
		parser:       parser,
	}
}

// reserve returns a new thread ID for a reference connection.
func (t *threads) reserve() uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	thread := t.next
	t.next++
	if t.next < firstThread {
		t.next = firstThread
	}
	return thread
}

// connect records the database thread of thread until release is called.
func (t *threads) connect(thread, database uint32) (release func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.database[thread] = database
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		delete(t.database, thread)
	}
}

// rewriteKill returns statement with the database thread in place of a reference's thread, or the
// error the reference gets for a thread the proxy does not know. The parser reads it as MySQL does, comments included.
func (t *threads) rewriteKill(statement []byte) (rewritten []byte, changed bool, err error) {
	if !t.mentionsKill.Match(statement) {
		return statement, false, nil
	}
	query, ok := t.parser.Parse(string(statement)).(normaliser.Query)
	if !ok || len(query.Statements) != 1 {
		return statement, false, nil
	}
	kill, ok := query.Statements[0].(*ast.KillStmt)
	if !ok || kill.TiDBExtension || kill.Expr != nil || kill.ConnectionID < uint64(firstThread) || kill.ConnectionID > math.MaxUint32 {
		// A thread the reference read from the database, such as with CONNECTION_ID(), or none.
		return statement, false, nil
	}
	t.mu.Lock()
	database, ok := t.database[uint32(kill.ConnectionID)]
	t.mu.Unlock()
	if !ok {
		return nil, false, mysql.NewError(mysql.ER_NO_SUCH_THREAD, fmt.Sprintf("Unknown thread id: %d", kill.ConnectionID))
	}
	if kill.Query {
		return fmt.Appendf(nil, "KILL QUERY %d", database), true, nil
	}
	return fmt.Appendf(nil, "KILL %d", database), true, nil
}

// relay passes packets between the reference and the database, rewriting KILL and
// refusing some commands, until either side closes. The observer sees both ways.
func relay(ctx context.Context, threads *threads, observer *observer, reference net.Conn, fromReference io.Reader, upstream net.Conn) {
	toReference := newLockedWriter(reference)
	queue := newOffers()
	offered := make(chan struct{})
	go func() {
		defer close(offered)
		queue.run()
	}()
	done := make(chan error, 2)
	go func() {
		done <- deliver(observer, upstream, toReference, queue)
	}()
	go func() {
		done <- forward(ctx, threads, observer, fromReference, upstream, toReference)
	}()
	if err := <-done; err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		logger.FromContext(ctx).DebugContext(ctx, "MySQL relay ended", "error", err)
	}
	_ = reference.Close()
	_ = upstream.Close()
	<-done
	// The reference has every queued reply, so they are still offered.
	queue.close()
	<-offered
	observer.close()
}

// deliver passes the database's bytes to the reference as they arrive, and queues
// each finished reply's offer once the reference has it.
func deliver(observer *observer, upstream io.Reader, toReference io.Writer, queue *offers) error {
	buffer := make([]byte, 32*1024)
	for {
		read, err := upstream.Read(buffer)
		if read > 0 {
			offer := observer.received(buffer[:read])
			if _, err := toReference.Write(buffer[:read]); err != nil {
				return errors.WithStack(err)
			}
			if offer, finished := offer.Get(); finished {
				queue.add(offer)
			}
		}
		if err != nil {
			return errors.WithStack(err)
		}
	}
}

// offers runs a connection's offers in order, away from the relay, so normalising a
// reply never delays the next. Their saved replies hold the budget while they wait.
type offers struct {
	mu      sync.Mutex
	pending []func()
	closed  bool
	wake    chan struct{}
}

func newOffers() *offers {
	return &offers{wake: make(chan struct{}, 1)}
}

// add queues an offer without waiting.
func (o *offers) add(offer func()) {
	o.mu.Lock()
	o.pending = append(o.pending, offer)
	o.mu.Unlock()
	o.signal()
}

// close lets run return once it has run every queued offer.
func (o *offers) close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	o.signal()
}

func (o *offers) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// run runs queued offers in the order they were added, until closed.
func (o *offers) run() {
	for range o.wake {
		o.mu.Lock()
		pending, closed := o.pending, o.closed
		o.pending = nil
		o.mu.Unlock()
		for _, offer := range pending {
			offer()
		}
		if closed {
			return
		}
	}
}

// forward passes the reference's packets to the database. A command starts with
// sequence number zero, and the packets that continue it never do.
func forward(ctx context.Context, threads *threads, observer *observer, from io.Reader, upstream io.Writer,
	toReference io.Writer,
) error {
	for {
		sent, err := readPacket(from)
		if err != nil {
			return err
		}
		whole := len(sent.payload) < maxPayload
		// The observer sees the statement the reference sent, as the candidate sends it.
		observed := sent.payload
		if sent.sequence == 0 && len(sent.payload) > 0 && whole {
			payload, refusal := threads.command(ctx, sent.payload)
			if refusal != nil {
				if err := writePacket(toReference, 1, errorPayload(refusal)); err != nil {
					return err
				}
				continue
			}
			sent.payload = payload
		}
		if sent.sequence == 0 {
			observer.sent(observed, whole)
		}
		if err := writePacket(upstream, sent.sequence, sent.payload); err != nil {
			return err
		}
	}
}

// command returns the command to send the database in place of payload, or the
// error that the reference gets instead.
func (t *threads) command(ctx context.Context, payload []byte) ([]byte, error) {
	switch payload[0] {
	case mysql.COM_QUERY:
		statement, changed, err := t.rewriteKill(payload[1:])
		if err != nil || !changed {
			return payload, err
		}
		return append([]byte{mysql.COM_QUERY}, statement...), nil
	case mysql.COM_CHANGE_USER:
		// The database connection always uses the proxy's own credentials.
		logger.FromContext(ctx).WarnContext(ctx, "Reference tried to change its MySQL user")
		return nil, mysql.NewError(mysql.ER_NOT_SUPPORTED_YET, "SPECTRE: changing the user is not supported")
	default:
		return payload, nil
	}
}

// lockedWriter writes whole packets from the relay's two directions without interleaving.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func newLockedWriter(w io.Writer) *lockedWriter {
	return &lockedWriter{w: w}
}

func (l *lockedWriter) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	written, err := l.w.Write(data)
	return written, errors.WithStack(err)
}

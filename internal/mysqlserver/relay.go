package mysqlserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"sync"

	"github.com/alecthomas/errors"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/logger"
)

// firstThread is the first thread ID the proxy gives references, far above the
// database's own, so a KILL names one or the other.
const firstThread uint32 = 3_000_000_000

// threads maps the thread IDs the proxy gave references to their database threads.
type threads struct {
	mu       sync.Mutex
	next     uint32
	database map[uint32]uint32
	kill     *regexp.Regexp
}

func newThreads() *threads {
	return &threads{
		next:     firstThread,
		database: map[uint32]uint32{},
		kill:     regexp.MustCompile(`(?is)^\s*KILL\s+((?:QUERY|CONNECTION)\s+)?(\d+)\s*;?\s*$`),
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

// rewriteKill returns statement with the database thread in place of a reference's
// thread, or the error the reference gets for a thread the proxy does not know.
func (t *threads) rewriteKill(statement []byte) (rewritten []byte, changed bool, err error) {
	match := t.kill.FindSubmatch(statement)
	if match == nil {
		return statement, false, nil
	}
	thread, err := strconv.ParseUint(string(match[2]), 10, 32)
	if err != nil || uint32(thread) < firstThread {
		// A thread the reference read from the database, such as with CONNECTION_ID().
		return statement, false, nil //nolint:nilerr // Too large for a thread the proxy gave.
	}
	t.mu.Lock()
	database, ok := t.database[uint32(thread)]
	t.mu.Unlock()
	if !ok {
		return nil, false, mysql.NewError(mysql.ER_NO_SUCH_THREAD, fmt.Sprintf("Unknown thread id: %d", thread))
	}
	return fmt.Appendf(nil, "KILL %s%d", match[1], database), true, nil
}

// relay passes packets between the reference and the database, rewriting KILL and
// refusing some commands, until either side closes. The observer sees both ways.
func relay(ctx context.Context, threads *threads, observer *observer, reference net.Conn, fromReference io.Reader, upstream net.Conn) {
	toReference := newLockedWriter(reference)
	done := make(chan error, 2)
	go func() {
		done <- deliver(observer, upstream, toReference)
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
}

// deliver passes the database's bytes to the reference as they arrive, and hands
// each finished reply to the observer's server once the reference has it.
func deliver(observer *observer, upstream io.Reader, toReference io.Writer) error {
	buffer := make([]byte, 32*1024)
	for {
		read, err := upstream.Read(buffer)
		if read > 0 {
			offer := observer.received(buffer[:read])
			if _, err := toReference.Write(buffer[:read]); err != nil {
				return errors.WithStack(err)
			}
			offer()
		}
		if err != nil {
			return errors.WithStack(err)
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
			observer.sent(sent.payload, whole)
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

package httpserver

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/future"
	"github.com/block/spectre/internal/proxy"
)

const bufferBlockSize = 4 * 1024

// bodySink receives a body's data as a teeBody reads it. closeWriter ends the
// data with io.EOF when it is complete; only its first call has any effect.
type bodySink interface {
	append(data []byte)
	closeWriter(err error)
}

// teeBody copies a body into its sink as the proxy reads it, so copying never
// delays forwarding. Closing before EOF ends the sink as incomplete.
type teeBody struct {
	source io.ReadCloser
	sink   bodySink
	eof    atomic.Bool
}

func newTeeBody(source io.ReadCloser, sink bodySink) *teeBody {
	return &teeBody{source: source, sink: sink}
}

func (b *teeBody) Read(data []byte) (int, error) {
	n, err := b.source.Read(data)
	if n > 0 {
		b.sink.append(data[:n])
	}
	switch {
	case errors.Is(err, io.EOF):
		b.eof.Store(true)
		b.sink.closeWriter(io.EOF)
		return n, io.EOF
	case err != nil:
		b.sink.closeWriter(err)
		return n, errors.Wrap(err, "read body")
	}
	return n, nil
}

func (b *teeBody) Close() error {
	err := b.source.Close()
	if !b.eof.Load() {
		b.sink.closeWriter(errors.Wrap(io.ErrUnexpectedEOF, "body closed before EOF"))
	}
	return errors.Wrap(err, "close body")
}

// streamBody is a one-producer, one-consumer queue that never blocks its producer.
// Its allocation is charged to the shared budget until read or discarded.
type streamBody struct {
	mu           sync.Mutex
	ready        *sync.Cond
	head         *bufferChunk
	tail         *bufferChunk
	offset       int
	allocated    int
	budget       *proxy.Budget
	overflow     func(error)
	abort        context.CancelFunc
	writerError  error
	writerClosed bool
	readerClosed bool
}

// bufferChunk keeps queue allocation in bounded blocks for exact budget accounting.
type bufferChunk struct {
	data []byte
	next *bufferChunk
}

// newStreamBody calls abort when its data ends incomplete, because the reader's
// request would be incomplete too.
func newStreamBody(budget *proxy.Budget, overflow func(error), abort context.CancelFunc) *streamBody {
	body := &streamBody{budget: budget, overflow: overflow, abort: abort}
	body.ready = sync.NewCond(&body.mu)
	return body
}

func (b *streamBody) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// The producer never waits for this reader, so a slow candidate cannot delay reference traffic.
	for b.head == nil && !b.writerClosed && !b.readerClosed {
		b.ready.Wait()
	}
	if b.head != nil {
		chunk := b.head
		n := copy(data, chunk.data[b.offset:])
		b.offset += n
		if b.offset == len(chunk.data) {
			b.head = chunk.next
			if b.head == nil {
				b.tail = nil
			}
			b.offset = 0
			b.allocated -= cap(chunk.data)
			b.budget.Release(cap(chunk.data))
		}
		return n, nil
	}
	if b.readerClosed {
		return 0, errors.Wrap(io.ErrClosedPipe, "read mirrored request body")
	}
	if errors.Is(b.writerError, io.EOF) {
		return 0, io.EOF
	}
	return 0, errors.Wrap(b.writerError, "read mirrored request body")
}

func (b *streamBody) append(data []byte) {
	b.mu.Lock()
	if b.readerClosed || b.writerClosed {
		b.mu.Unlock()
		return
	}
	for len(data) > 0 {
		if b.tail == nil || len(b.tail.data) == cap(b.tail.data) {
			capacity := min(bufferBlockSize, b.budget.Limit())
			if !b.budget.Reserve(capacity) {
				overflowError := errors.Errorf("candidate request buffer exceeded %d bytes", b.budget.Limit())
				b.writerClosed = true
				b.writerError = overflowError
				b.clearBuffer()
				b.ready.Broadcast()
				b.mu.Unlock()
				// Quarantine may cancel this reader, so invoke it after releasing the body lock.
				b.overflow(overflowError)
				return
			}
			chunk := &bufferChunk{data: make([]byte, 0, capacity)}
			if b.tail == nil {
				b.head = chunk
			} else {
				b.tail.next = chunk
			}
			b.tail = chunk
			b.allocated += capacity
		}
		available := cap(b.tail.data) - len(b.tail.data)
		written := min(available, len(data))
		start := len(b.tail.data)
		b.tail.data = b.tail.data[:start+written]
		copy(b.tail.data[start:], data[:written])
		data = data[written:]
	}
	b.ready.Signal()
	b.mu.Unlock()
}

func (b *streamBody) Close() error {
	b.closeReader()
	return nil
}

func (b *streamBody) closeReader() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.readerClosed = true
	b.clearBuffer()
	b.ready.Broadcast()
}

func (b *streamBody) closeWriter(err error) {
	b.mu.Lock()
	if b.writerClosed {
		b.mu.Unlock()
		return
	}
	b.writerClosed = true
	b.writerError = err
	b.ready.Broadcast()
	b.mu.Unlock()
	if !errors.Is(err, io.EOF) {
		b.abort()
	}
}

func (b *streamBody) clearBuffer() {
	// Callers hold b.mu so chunks cannot be consumed while their budget is released.
	b.budget.Release(b.allocated)
	b.head = nil
	b.tail = nil
	b.offset = 0
	b.allocated = 0
}

// requestCapture keeps the first limit bytes of a request body, so the engine
// can decode the body once the upstream has read all of it.
type requestCapture struct {
	limit    int
	captured *future.Future[capturedBody]

	// mu guards the body until it is captured. Appends check captured under mu,
	// so the captured body never changes.
	mu       sync.Mutex
	body     []byte
	overflow bool
}

// capturedBody is a request body as the upstream finished reading it.
type capturedBody struct {
	body     []byte
	overflow bool
	// err is why the body is incomplete.
	err error
}

func newRequestCapture(limit int) *requestCapture {
	return &requestCapture{limit: limit, captured: future.New[capturedBody]()}
}

func (c *requestCapture) append(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, closed := c.captured.Get(); closed {
		return
	}
	remaining := c.limit - len(c.body)
	if len(data) > remaining {
		c.overflow = true
		data = data[:max(remaining, 0)]
	}
	c.body = append(c.body, data...)
}

func (c *requestCapture) closeWriter(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if errors.Is(err, io.EOF) {
		err = nil
	}
	c.captured.Set(capturedBody{body: c.body, overflow: c.overflow, err: err})
}

// wait returns the whole captured body, which may be truncated to the limit.
func (c *requestCapture) wait(ctx context.Context) (body []byte, overflow bool, err error) {
	captured, err := c.captured.Wait(ctx)
	if err != nil {
		return nil, false, errors.Wrap(err, "wait for request body")
	}
	if captured.err != nil {
		return nil, false, errors.Wrap(captured.err, "request body was not fully read")
	}
	return captured.body, captured.overflow, nil
}

package ingress

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/alecthomas/errors"
)

const bufferBlockSize = 4 * 1024

type mirrorBody struct {
	source io.ReadCloser
	mirror *streamBody
	abort  context.CancelFunc
	eof    atomic.Bool
}

func newMirrorBody(source io.ReadCloser, mirror *streamBody, abort context.CancelFunc) *mirrorBody {
	return &mirrorBody{source: source, mirror: mirror, abort: abort}
}

func (b *mirrorBody) Read(data []byte) (int, error) {
	n, err := b.source.Read(data)
	if n > 0 {
		b.mirror.append(data[:n])
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			b.eof.Store(true)
		}
		b.mirror.closeWriter(err)
		if !errors.Is(err, io.EOF) {
			err = errors.Wrap(err, "read source request body")
		}
	}
	return n, err
}

func (b *mirrorBody) Close() error {
	err := b.source.Close()
	mirrorError := err
	if mirrorError == nil && !b.eof.Load() {
		mirrorError = io.ErrUnexpectedEOF
	}
	b.mirror.closeWriter(mirrorError)
	if mirrorError != nil {
		b.abort()
	}
	return errors.Wrap(err, "close source request body")
}

type streamBody struct {
	mu           sync.Mutex
	ready        *sync.Cond
	head         *bufferChunk
	tail         *bufferChunk
	offset       int
	allocated    int
	budget       *bufferBudget
	overflow     func(error)
	writerError  error
	writerClosed bool
	readerClosed bool
}

type bufferChunk struct {
	data []byte
	next *bufferChunk
}

func newStreamBody(budget *bufferBudget, overflow func(error)) *streamBody {
	body := &streamBody{budget: budget, overflow: overflow}
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
			b.budget.release(cap(chunk.data))
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
			capacity := min(bufferBlockSize, b.budget.capacityLimit())
			if !b.budget.reserve(capacity) {
				overflowError := errors.Errorf("candidate request buffer exceeded %d bytes", b.budget.capacityLimit())
				b.writerClosed = true
				b.writerError = overflowError
				b.clearBuffer()
				b.ready.Broadcast()
				b.mu.Unlock()
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
	defer b.mu.Unlock()
	if b.writerClosed {
		return
	}
	b.writerClosed = true
	if err == nil {
		err = io.EOF
	}
	b.writerError = err
	b.ready.Broadcast()
}

func (b *streamBody) clearBuffer() {
	b.budget.release(b.allocated)
	b.head = nil
	b.tail = nil
	b.offset = 0
	b.allocated = 0
}

type bufferBudget struct {
	mu    sync.Mutex
	limit int
	used  int
}

func newBufferBudget(limit int) *bufferBudget {
	return &bufferBudget{limit: limit}
}

func (b *bufferBudget) capacityLimit() int {
	return b.limit
}

func (b *bufferBudget) reserve(size int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if size > b.limit-b.used {
		return false
	}
	b.used += size
	return true
}

func (b *bufferBudget) release(size int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used -= size
}

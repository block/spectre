package egress

import (
	"io"
	"sync"

	"github.com/alecthomas/errors"
)

// captureBody copies a reference request body as the upstream transport reads it,
// so hashing never delays forwarding. The transport may close it concurrently with a read.
type captureBody struct {
	source io.ReadCloser
	limit  int
	// done receives the captured body exactly once, at EOF or on close.
	done func(body []byte, overflow bool, complete bool)

	mu       sync.Mutex
	body     []byte
	overflow bool
	finished bool
}

func newCaptureBody(source io.ReadCloser, limit int, done func(body []byte, overflow bool, complete bool)) *captureBody {
	return &captureBody{source: source, limit: limit, done: done}
}

func (b *captureBody) Read(data []byte) (int, error) {
	n, err := b.source.Read(data)
	b.append(data[:n])
	switch {
	case errors.Is(err, io.EOF):
		b.finish(true)
		return n, io.EOF
	case err != nil:
		b.finish(false)
		return n, errors.Wrap(err, "read reference request body")
	}
	return n, nil
}

// Close before EOF leaves the capture incomplete, because the upstream never
// received the rest of the body.
func (b *captureBody) Close() error {
	b.finish(false)
	return errors.Wrap(b.source.Close(), "close reference request body")
}

func (b *captureBody) append(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// The hash owns the captured body once it is finished.
	if b.finished {
		return
	}
	remaining := b.limit - len(b.body)
	if len(data) > remaining {
		b.overflow = true
		data = data[:max(remaining, 0)]
	}
	b.body = append(b.body, data...)
}

func (b *captureBody) finish(complete bool) {
	b.mu.Lock()
	if b.finished {
		b.mu.Unlock()
		return
	}
	b.finished = true
	body, overflow := b.body, b.overflow
	b.mu.Unlock()
	b.done(body, overflow, complete)
}

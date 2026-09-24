package ingress

import (
	"io"
	"sync"

	"github.com/alecthomas/errors"
)

type mirrorBody struct {
	source io.ReadCloser
	mirror *streamBody
}

func newMirrorBody(source io.ReadCloser, mirror *streamBody) *mirrorBody {
	return &mirrorBody{source: source, mirror: mirror}
}

func (b *mirrorBody) Read(data []byte) (int, error) {
	n, err := b.source.Read(data)
	if n > 0 {
		b.mirror.append(data[:n])
	}
	if err != nil {
		b.mirror.closeWriter(err)
		if !errors.Is(err, io.EOF) {
			err = errors.Wrap(err, "read source request body")
		}
	}
	return n, err
}

func (b *mirrorBody) Close() error {
	err := b.source.Close()
	b.mirror.closeWriter(err)
	return errors.Wrap(err, "close source request body")
}

type streamBody struct {
	mu           sync.Mutex
	ready        *sync.Cond
	data         []byte
	writerError  error
	writerClosed bool
	readerClosed bool
}

func newStreamBody() *streamBody {
	body := &streamBody{}
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
	for len(b.data) == 0 && !b.writerClosed && !b.readerClosed {
		b.ready.Wait()
	}
	if len(b.data) > 0 {
		n := copy(data, b.data)
		b.data = b.data[n:]
		if len(b.data) == 0 {
			b.data = nil
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
	defer b.mu.Unlock()
	if b.readerClosed {
		return
	}
	b.data = append(b.data, data...)
	b.ready.Signal()
}

func (b *streamBody) Close() error {
	b.closeReader()
	return nil
}

func (b *streamBody) closeReader() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.readerClosed = true
	b.data = nil
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

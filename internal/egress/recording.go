package egress

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/proxy"
)

// recording captures one reference response so a matching candidate can replay it.
// The reference transport writes it once, and replay reads it only after done closes.
type recording struct {
	budget *proxy.Budget
	done   chan struct{}

	mu            sync.Mutex
	finished      bool
	status        int
	header        http.Header
	contentLength int64
	body          []byte
	trailer       http.Header
	err           error
	overBudget    bool
	reserved      int
	released      bool
}

func newRecording(budget *proxy.Budget) *recording {
	return &recording{budget: budget, done: make(chan struct{})}
}

// start records the response head once the upstream has answered.
func (r *recording) start(response *http.Response) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = response.StatusCode
	r.header = response.Header.Clone()
	r.contentLength = response.ContentLength
}

// write appends body data, dropping the whole body once it exceeds the budget.
func (r *recording) write(data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released || r.overBudget || len(data) == 0 {
		return
	}
	if !r.budget.Reserve(len(data)) {
		r.overBudget = true
		r.dropBody()
		return
	}
	r.reserved += len(data)
	r.body = append(r.body, data...)
}

// finish completes the recording. Only the first call has any effect.
func (r *recording) finish(trailer http.Header, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	r.trailer = trailer.Clone()
	r.err = err
	close(r.done)
}

// release returns the recorded body to the budget. Replay must have finished.
func (r *recording) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = true
	r.dropBody()
}

// dropBody frees the body and its reservation. Callers hold r.mu.
func (r *recording) dropBody() {
	r.budget.Release(r.reserved)
	r.reserved = 0
	r.body = nil
}

// wait blocks until the reference response is complete, then reports whether
// it can be replayed.
func (r *recording) wait(ctx context.Context) error {
	select {
	case <-r.done:
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "wait for reference response")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overBudget {
		return errors.New("reference response exceeds the recording budget")
	}
	return nil
}

// RoundTrip replays the finished recording, including a reference transport
// failure, so the candidate sees what the reference saw.
func (r *recording) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		if err := request.Body.Close(); err != nil {
			return nil, errors.Wrap(err, "close replayed request body")
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, errors.Wrap(r.err, "replay reference failure")
	}
	return &http.Response{
		Status:        strconv.Itoa(r.status) + " " + http.StatusText(r.status),
		StatusCode:    r.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        r.header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(r.body)),
		ContentLength: r.contentLength,
		Trailer:       r.trailer.Clone(),
		Request:       request,
	}, nil
}

// recordingTransport records every response it forwards into one recording.
type recordingTransport struct {
	next      http.RoundTripper
	recording *recording
}

func newRecordingTransport(next http.RoundTripper, recording *recording) *recordingTransport {
	return &recordingTransport{next: next, recording: recording}
}

// RoundTrip forwards the request and tees the response body into the recording.
func (t *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	if err != nil {
		t.recording.finish(nil, err)
		return nil, errors.WithStack(err)
	}
	if response.StatusCode == http.StatusSwitchingProtocols {
		// The reverse proxy needs the raw upgraded body, so it cannot be teed.
		t.recording.finish(nil, errors.New("protocol upgrades cannot be replayed"))
		return response, nil
	}
	t.recording.start(response)
	response.Body = newRecordingBody(response, t.recording)
	return response, nil
}

// recordingBody copies a response body into its recording as the reverse proxy
// reads it. The proxy closes the body before reading trailers, so they are captured on close.
type recordingBody struct {
	source    io.ReadCloser
	response  *http.Response
	recording *recording
	eof       atomic.Bool
}

func newRecordingBody(response *http.Response, recording *recording) *recordingBody {
	return &recordingBody{source: response.Body, response: response, recording: recording}
}

func (b *recordingBody) Read(data []byte) (int, error) {
	n, err := b.source.Read(data)
	b.recording.write(data[:n])
	switch {
	case errors.Is(err, io.EOF):
		b.eof.Store(true)
		return n, io.EOF
	case err != nil:
		b.recording.finish(nil, err)
		return n, errors.Wrap(err, "read reference response body")
	}
	return n, nil
}

func (b *recordingBody) Close() error {
	err := b.source.Close()
	if b.eof.Load() {
		b.recording.finish(b.response.Trailer, nil)
	} else {
		b.recording.finish(nil, errors.New("reference response body closed before EOF"))
	}
	return errors.Wrap(err, "close reference response body")
}

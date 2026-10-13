package httpserver

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/httpserver/message"
	"github.com/block/spectre/internal/proxy"
)

// recording captures one response, both to decode it for comparison and to
// replay it to a candidate. The transport writes it once; readers wait for it to finish.
type recording struct {
	budget  *proxy.Budget
	started time.Time

	mu            sync.Mutex
	finished      bool
	firstByte     time.Time
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

// newRecording reserves body bytes from budget, which bounds either one response
// or every recording that shares it.
func newRecording(budget *proxy.Budget) *recording {
	return &recording{budget: budget, started: time.Now()}
}

// start records the response head once the upstream has answered.
func (r *recording) start(response *http.Response) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.firstByte = time.Now()
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
}

// Release returns the recorded body to the budget. Replay must have finished.
func (r *recording) Release() {
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

// complete reports whether the finished recording holds the whole response body.
func (r *recording) complete() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overBudget {
		return errors.Errorf("response exceeds the %d byte recording budget", r.budget.Limit())
	}
	return nil
}

// failure returns the transport failure, if the response did not finish.
func (r *recording) failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return errors.Wrap(r.err, "response failed")
	}
	return nil
}

// latency returns the time to the response head, or zero if none arrived.
func (r *recording) latency() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.firstByte.IsZero() {
		return 0
	}
	return r.firstByte.Sub(r.started)
}

// response returns the finished response for decoding, or the transport failure.
func (r *recording) response() (message.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return message.Response{}, errors.Wrap(r.err, "response failed")
	}
	return message.Response{StatusCode: r.status, Header: r.header, Trailer: r.trailer, Body: r.body}, nil
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
	response.Body = newTeeBody(response.Body, newRecordingSink(response, t.recording))
	return response, nil
}

// recordingSink records a response body. The response's trailers are complete
// once its body reaches EOF.
type recordingSink struct {
	response  *http.Response
	recording *recording
}

func newRecordingSink(response *http.Response, recording *recording) recordingSink {
	return recordingSink{response: response, recording: recording}
}

func (s recordingSink) append(data []byte) {
	s.recording.write(data)
}

func (s recordingSink) closeWriter(err error) {
	if errors.Is(err, io.EOF) {
		s.recording.finish(s.response.Trailer, nil)
		return
	}
	s.recording.finish(nil, err)
}

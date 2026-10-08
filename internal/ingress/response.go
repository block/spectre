package ingress

import (
	"net/http"
	"time"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison"
)

// captureResponseWriter forwards a response unchanged while retaining a bounded copy.
// One proxy invocation owns each writer, so capture state needs no synchronization.
type captureResponseWriter struct {
	http.ResponseWriter
	limit     int
	status    int
	body      []byte
	overflow  bool
	started   time.Time
	firstByte time.Time
}

func newCaptureResponseWriter(writer http.ResponseWriter, limit int) *captureResponseWriter {
	return &captureResponseWriter{ResponseWriter: writer, limit: limit, started: time.Now()}
}

func (w *captureResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.firstByte = time.Now()
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *captureResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
		w.firstByte = time.Now()
	}
	remaining := w.limit - len(w.body)
	if remaining > 0 {
		captured := min(remaining, len(data))
		w.body = append(w.body, data[:captured]...)
	}
	if len(data) > remaining {
		w.overflow = true
	}
	written, err := w.ResponseWriter.Write(data)
	return written, errors.Wrap(err, "write captured response")
}

func (w *captureResponseWriter) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *captureResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Latency returns the backend's time to first response byte, or zero if none was written.
func (w *captureResponseWriter) Latency() time.Duration {
	if w.firstByte.IsZero() {
		return 0
	}
	return w.firstByte.Sub(w.started)
}

// Response returns detached headers and body for asynchronous comparison.
func (w *captureResponseWriter) Response() comparison.Response {
	return comparison.Response{
		StatusCode: w.status,
		Header:     w.Header().Clone(),
		Body:       append([]byte(nil), w.body...),
		Overflow:   w.overflow,
		Latency:    w.Latency(),
	}
}

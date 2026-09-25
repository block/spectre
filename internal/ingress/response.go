package ingress

import (
	"net/http"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/comparison"
)

// captureResponseWriter forwards a response unchanged while retaining a bounded copy.
// One proxy invocation owns each writer, so capture state needs no synchronization.
type captureResponseWriter struct {
	http.ResponseWriter
	limit    int
	status   int
	body     []byte
	overflow bool
}

func newCaptureResponseWriter(writer http.ResponseWriter, limit int) *captureResponseWriter {
	return &captureResponseWriter{ResponseWriter: writer, limit: limit}
}

func (w *captureResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *captureResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
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

// Response returns detached headers and body for asynchronous comparison.
func (w *captureResponseWriter) Response() comparison.Response {
	return comparison.Response{
		StatusCode: w.status,
		Header:     w.Header().Clone(),
		Body:       append([]byte(nil), w.body...),
		Overflow:   w.overflow,
	}
}

package stream

import (
	"io"
	"net/http"
	"time"

	"github.com/alecthomas/errors"
)

// EndEarly lets next end a client stream before the client does. Otherwise an
// HTTP/1 server reads the rest of the request before it replies.
func EndEarly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.Body = newUnendedBody(request.Body, http.NewResponseController(writer))
		next.ServeHTTP(writer, request)
	})
}

// unendedBody is a request body that the client may not have ended.
type unendedBody struct {
	io.ReadCloser
	controller *http.ResponseController
	ended      bool
}

func newUnendedBody(body io.ReadCloser, controller *http.ResponseController) *unendedBody {
	return &unendedBody{ReadCloser: body, controller: controller}
}

func (b *unendedBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	b.ended = b.ended || errors.Is(err, io.EOF)
	return n, err //nolint:wrapcheck // Readers must return io.EOF itself.
}

// Close stops reading a body the client has not ended, so the server replies
// at once and then closes the connection, which tells the client.
func (b *unendedBody) Close() error {
	if !b.ended {
		_ = b.controller.SetReadDeadline(time.Now()) //nolint:errcheck // Without a deadline, closing only takes longer.
	}
	return errors.WithStack(b.ReadCloser.Close())
}

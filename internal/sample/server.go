package sample

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/middleware/health"
	"github.com/block/spectre/internal/middleware/logging"
)

// Server serves sample Connect requests and HTTP health checks on one listener.
type Server struct {
	health *health.Handler
}

// NewServer constructs a sample server around a configured Connect handler.
func NewServer(connectHandler http.Handler, log *slog.Logger) *Server {
	handler := logging.New(connectHandler, log)
	return &Server{health: health.New(handler)}
}

// Serve accepts sample traffic until the context is cancelled or the server fails.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Handler:           s,
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return context.WithoutCancel(ctx)
		},
	}
	// Readiness becomes false before connections close so observers stop routing work.
	s.health.SetReady(true)
	stopServer := context.AfterFunc(ctx, func() {
		s.health.SetReady(false)
		_ = server.Close()
	})
	defer stopServer()
	err := server.Serve(listener)
	s.health.SetReady(false)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return errors.Wrap(err, "serve sample HTTP")
}

// ServeHTTP serves sample Connect requests and health checks.
func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.health.ServeHTTP(writer, request)
}

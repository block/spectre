package server

import (
	"context"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/httpserver/middleware/health"
	"github.com/block/spectre/internal/httpserver/middleware/logging"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
	pb "github.com/block/spectre/spectrepb"
	"github.com/block/spectre/spectrepb/spectrepbconnect"
)

// Service serves the SPECTRE service API.
type Service struct {
	store *store
}

// NewService returns a SPECTRE server with no divergences.
func NewService() *Service {
	return &Service{store: newStore()}
}

// Handler serves the API.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(spectrepbconnect.NewSpectreServiceHandler(s))
	return mux
}

// Quarantine records that a mirror quarantined its candidate.
func (s *Service) Quarantine(_ context.Context, request *connect.Request[pb.QuarantineRequest]) (*connect.Response[pb.QuarantineResponse], error) {
	if err := s.store.record(request.Msg.GetDivergence()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&pb.QuarantineResponse{}), nil
}

// ListDivergences returns the latest quarantine of each mirror direction.
func (s *Service) ListDivergences(_ context.Context, request *connect.Request[pb.ListDivergencesRequest]) (*connect.Response[pb.ListDivergencesResponse], error) {
	return connect.NewResponse(&pb.ListDivergencesResponse{Divergences: s.store.listDivergences(request.Msg)}), nil
}

// ClearDivergences removes the divergences of one service environment.
func (s *Service) ClearDivergences(_ context.Context, request *connect.Request[pb.ClearDivergencesRequest]) (*connect.Response[pb.ClearDivergencesResponse], error) {
	if err := s.store.clear(request.Msg.GetService(), request.Msg.GetEnvironment()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&pb.ClearDivergencesResponse{}), nil
}

// Serve runs a SPECTRE server until ctx is cancelled. Mesh mTLS secures
// it, so it serves plaintext HTTP/1 and unencrypted HTTP/2.
func Serve(ctx context.Context, config ServeConfig) error {
	listener, err := netaddr.ParseListen(config.Listen).Listen(ctx)
	if err != nil {
		return errors.Wrap(err, "listen for SPECTRE server requests")
	}
	handler := health.New(logging.New(NewService().Handler(), logger.EventReceived))
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Handler:           handler,
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	stop := context.AfterFunc(ctx, func() {
		handler.SetReady(false)
		_ = server.Close() //nolint:errcheck // Close only reports listener errors, and the server is stopping.
	})
	defer stop()
	handler.SetReady(true)
	logger.FromContext(ctx).InfoContext(ctx, "SPECTRE server listening", "address", listener.Addr().String())
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return errors.Wrap(err, "serve SPECTRE server requests")
	}
	return nil
}

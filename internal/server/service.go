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
	"github.com/block/spectre/internal/server/storage"
	pb "github.com/block/spectre/spectrepb"
	"github.com/block/spectre/spectrepb/spectrepbconnect"
)

// Service serves the SPECTRE service API.
type Service struct {
	store storage.Store
}

// NewService returns a SPECTRE server that keeps divergences in store.
func NewService(store storage.Store) *Service {
	return &Service{store: store}
}

// Handler serves the API.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(spectrepbconnect.NewSpectreServiceHandler(s))
	return mux
}

// Quarantine records that a mirror quarantined its candidate.
func (s *Service) Quarantine(ctx context.Context, request *connect.Request[pb.QuarantineRequest]) (*connect.Response[pb.QuarantineResponse], error) {
	divergence := request.Msg.GetDivergence()
	if err := validateDivergence(divergence); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.store.Record(ctx, divergence); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&pb.QuarantineResponse{}), nil
}

// ListDivergences returns the latest quarantine of each mirror direction.
func (s *Service) ListDivergences(ctx context.Context, request *connect.Request[pb.ListDivergencesRequest]) (*connect.Response[pb.ListDivergencesResponse], error) {
	divergences, err := s.store.List(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&pb.ListDivergencesResponse{Divergences: filterDivergences(divergences, request.Msg)}), nil
}

// ClearDivergences removes the divergences of one service environment.
func (s *Service) ClearDivergences(ctx context.Context, request *connect.Request[pb.ClearDivergencesRequest]) (*connect.Response[pb.ClearDivergencesResponse], error) {
	service, environment := request.Msg.GetService(), request.Msg.GetEnvironment()
	if service == "" || environment == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("clearing divergences needs a service and environment"))
	}
	if err := s.store.Clear(ctx, service, environment); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
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
	store, err := storage.Open(ctx, config.Storage)
	if err != nil {
		return errors.Wrap(err, "open divergence storage")
	}
	defer store.Close()
	handler := health.New(logging.New(NewService(store).Handler(), logger.EventReceived))
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

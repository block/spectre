package coordinator

import (
	"context"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/alecthomas/errors"

	pb "github.com/block/spectre/internal/coordinator/pb"
	"github.com/block/spectre/internal/coordinator/pb/coordinatorpbconnect"
	"github.com/block/spectre/internal/httpserver/middleware/health"
	"github.com/block/spectre/internal/httpserver/middleware/logging"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
)

// Service serves the mirror and query APIs of the coordination service.
type Service struct {
	store *store
}

// NewService returns a coordination service with no divergences.
func NewService() *Service {
	return &Service{store: newStore()}
}

// Handler serves both APIs.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(coordinatorpbconnect.NewMirrorServiceHandler(s))
	mux.Handle(coordinatorpbconnect.NewQueryServiceHandler(s))
	return mux
}

// Report records the reporting mirror's quarantines.
func (s *Service) Report(_ context.Context, request *connect.Request[pb.ReportRequest]) (*connect.Response[pb.ReportResponse], error) {
	if err := s.store.report(request.Msg.GetMirror()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&pb.ReportResponse{}), nil
}

// ListDivergences returns quarantines, grouping identical ones across mirrors.
func (s *Service) ListDivergences(_ context.Context, request *connect.Request[pb.ListDivergencesRequest]) (*connect.Response[pb.ListDivergencesResponse], error) {
	return connect.NewResponse(&pb.ListDivergencesResponse{Divergences: s.store.listDivergences(request.Msg)}), nil
}

// Serve runs a coordination service until ctx is cancelled. Mesh mTLS secures
// it, so it serves plaintext HTTP/1 and unencrypted HTTP/2.
func Serve(ctx context.Context, config ServeConfig) error {
	listener, err := netaddr.ParseListen(config.Listen).Listen(ctx)
	if err != nil {
		return errors.Wrap(err, "listen for coordination requests")
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
	logger.FromContext(ctx).InfoContext(ctx, "Coordination service listening", "address", listener.Addr().String())
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return errors.Wrap(err, "serve coordination requests")
	}
	return nil
}

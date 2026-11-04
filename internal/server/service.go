package server

import (
	"context"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/alecthomas/errors"

	"github.com/block/spectre/internal/httpserver/middleware/health"
	"github.com/block/spectre/internal/httpserver/middleware/logging"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/server/storage"
	"github.com/block/spectre/internal/server/wire"
	pb "github.com/block/spectre/spectrepb"
	"github.com/block/spectre/spectrepb/spectrepbconnect"
)

// Service serves the SPECTRE service API.
type Service struct {
	store storage.Store
	ttl   time.Duration
}

// NewService returns a SPECTRE server that keeps mirror states and divergences
// in store, and lists the states received within ttl.
func NewService(store storage.Store, ttl time.Duration) *Service {
	return &Service{store: store, ttl: ttl}
}

// Handler serves the API.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	server := connect.NewServer()
	spectrepbconnect.RegisterSpectreServiceHandler(server, s)
	connecthttp.Mount(mux, server)
	return mux
}

// Update records the latest state of a mirror, and the divergence of each
// of its quarantines.
func (s *Service) Update(ctx context.Context, request *pb.UpdateRequest) (*pb.UpdateResponse, error) {
	state, err := wire.StateFromMessage(request.GetState())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	if err := s.store.Put(ctx, state, time.Now()); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	for direction, quarantine := range state.Quarantines {
		divergence := model.Divergence{
			Service:     state.Service,
			Environment: state.Environment,
			Revision:    state.Revision,
			Direction:   direction,
			Quarantine:  quarantine,
		}
		if err := s.store.Record(ctx, divergence); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
		}
	}
	return &pb.UpdateResponse{}, nil
}

// ListDivergences returns the states of the mirrors that sent a heartbeat
// within the TTL, and every recorded divergence.
func (s *Service) ListDivergences(ctx context.Context, request *pb.ListDivergencesRequest) (*pb.ListDivergencesResponse, error) {
	states, err := s.store.States(ctx, time.Now().Add(-s.ttl))
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	divergences, err := s.store.Divergences(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	filter := wire.FilterFromMessage(request)
	return wire.ListResponse(filterStates(states, filter), filterDivergences(divergences, filter)), nil
}

// ClearDivergences removes the divergences of one service environment.
func (s *Service) ClearDivergences(ctx context.Context, request *pb.ClearDivergencesRequest) (*pb.ClearDivergencesResponse, error) {
	service, environment := request.GetService(), request.GetEnvironment()
	if service == "" || environment == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "clearing divergences needs a service and environment")
	}
	if err := s.store.Clear(ctx, service, environment, time.Now()); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	return &pb.ClearDivergencesResponse{}, nil
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
		return errors.Wrap(err, "open state storage")
	}
	defer store.Close()
	handler := health.New(logging.New(NewService(store, config.StateTTL).Handler(), logger.EventReceived))
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

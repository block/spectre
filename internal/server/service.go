package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/httpserver/middleware/endearly"
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
	return endearly.New(mux)
}

// Update keeps the state a mirror streams. Every message stores the whole state,
// and a state or quarantine also records the divergence of each quarantine.
func (s *Service) Update(ctx context.Context, updates spectrepbconnect.SpectreServiceUpdateServerStream) (*pb.UpdateResponse, error) {
	var state Option[model.State]
	var measured time.Time
	for {
		message, err := updates.Receive()
		if errors.Is(err, io.EOF) {
			return &pb.UpdateResponse{}, nil
		}
		if err != nil {
			return nil, errors.WithStack(err)
		}
		current, err := nextState(state, message)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
		}
		now := time.Now()
		if message.GetState() != nil {
			measured = now
		}
		state = Some(current)
		// The mirror measured its uptime when it sent the whole state.
		stored := current
		stored.Uptime += now.Sub(measured)
		if err := s.store.Put(ctx, stored, now); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
		}
		if message.GetKeepalive() != nil || message.GetCalls() != nil {
			continue
		}
		if err := s.recordQuarantines(ctx, current); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
		}
	}
}

// recordQuarantines records the divergence of each quarantine state holds.
func (s *Service) recordQuarantines(ctx context.Context, state model.State) error {
	for direction, quarantine := range state.Quarantines {
		divergence := model.Divergence{
			Service:     state.Service,
			Environment: state.Environment,
			Revision:    state.Revision,
			Direction:   direction,
			Quarantine:  quarantine,
		}
		if err := s.store.Record(ctx, divergence); err != nil {
			return errors.WithStack(err)
		}
	}
	return nil
}

// ListDivergences returns the states of the mirrors that sent an update within
// the TTL, and every recorded divergence.
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

// nextState applies a streamed update to the state a stream has built. A
// stream must begin with the whole state, and a keepalive leaves it as it is.
func nextState(state Option[model.State], message *pb.UpdateRequest) (model.State, error) {
	current, started := state.Get()
	if message.GetKeepalive() != nil {
		if !started {
			return model.State{}, errors.New("an update stream must begin with the mirror's state")
		}
		return current, nil
	}
	update, err := wire.UpdateFromMessage(message)
	if err != nil {
		return model.State{}, errors.WithStack(err)
	}
	switch update := update.(type) {
	case model.State:
		return update, nil
	case model.QuarantineUpdate:
		if !started {
			return model.State{}, errors.New("an update stream must begin with the mirror's state")
		}
		return current.Quarantined(update.Direction, update.Quarantine), nil
	case model.Calls:
		if !started {
			return model.State{}, errors.New("an update stream must begin with the mirror's state")
		}
		current.Calls = update
		return current, nil
	}
	return current, nil
}

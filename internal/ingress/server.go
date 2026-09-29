package ingress

import (
	"context"
	"net"
	"net/http"

	"github.com/alecthomas/errors"
	"golang.org/x/net/netutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/block/spectre/internal/schema"
)

// Serve accepts ingress traffic until the context is cancelled or the server fails.
func (h *Handler) Serve(ctx context.Context, listener net.Listener) error {
	// Explicit shutdown owns request lifetime after the signal starts shutdown ordering.
	serverContext := context.WithoutCancel(ctx)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Handler:           h,
		Protocols:         protocols,
		ReadHeaderTimeout: h.config.ReadHeaderTimeout,
		IdleTimeout:       h.config.IdleTimeout,
		MaxHeaderBytes:    h.config.MaxHeaderBytes,
		BaseContext: func(net.Listener) context.Context {
			return serverContext
		},
	}
	limitedListener := netutil.LimitListener(listener, h.config.MaxConnections)
	serveDone := make(chan error, 1)
	// Readiness remains false until the comparison schema is configured.
	defer h.health.SetReady(false)
	go func() { serveDone <- server.Serve(limitedListener) }()
	h.log.InfoContext(ctx, "Ingress proxy listening", "address", listener.Addr().String())
	h.configureSchema(ctx)

	select {
	case err := <-serveDone:
		h.health.SetReady(false)
		shutdownContext, cancel := context.WithTimeout(serverContext, h.config.ShutdownTimeout)
		defer cancel()
		shutdownErrors := make([]error, 0, 4)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			shutdownErrors = append(shutdownErrors, errors.Wrap(err, "serve ingress HTTP"))
		}
		if err := server.Shutdown(shutdownContext); err != nil {
			shutdownErrors = append(shutdownErrors, errors.Wrap(err, "shut down HTTP server"))
			if closeErr := server.Close(); closeErr != nil {
				shutdownErrors = append(shutdownErrors, errors.Wrap(closeErr, "close HTTP server"))
			}
		}
		if err := h.Shutdown(shutdownContext); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
		return errors.Join(shutdownErrors...)
	case <-ctx.Done():
		h.health.SetReady(false)
		shutdownContext, cancel := context.WithTimeout(serverContext, h.config.ShutdownTimeout)
		defer cancel()
		shutdownErrors := make([]error, 0, 4)
		// Stop inbound handlers first so candidate admission cannot race with shutdown.
		if err := server.Shutdown(shutdownContext); err != nil {
			shutdownErrors = append(shutdownErrors, errors.Wrap(err, "shut down HTTP server"))
			if closeErr := server.Close(); closeErr != nil {
				shutdownErrors = append(shutdownErrors, errors.Wrap(closeErr, "close HTTP server"))
			}
		}
		if err := h.Shutdown(shutdownContext); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
		if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
			shutdownErrors = append(shutdownErrors, errors.Wrap(err, "serve ingress HTTP"))
		}
		return errors.Join(shutdownErrors...)
	}
}

// configureSchema makes readiness contingent on one schema shared by both backends.
// Static descriptors are identical for both, so only reflected ones are compared.
func (h *Handler) configureSchema(ctx context.Context) {
	reflectionContext, cancel := context.WithTimeout(ctx, h.config.ReflectionTimeout)
	defer cancel()
	set := h.static
	if h.config.Reflection {
		reference, candidate, err := h.loadDescriptors(reflectionContext)
		if err != nil {
			h.log.ErrorContext(ctx, "Backend descriptor loading failed", "error", err)
			return
		}
		if !proto.Equal(reference, candidate) {
			h.log.ErrorContext(ctx, "Backend descriptors differ")
			return
		}
		set, err = schema.Merge(reference, h.static)
		if err != nil {
			h.log.ErrorContext(ctx, "Backend descriptors conflict with static descriptors", "error", err)
			return
		}
	}
	if err := h.comparator.Configure(reflectionContext, set); err != nil {
		h.log.ErrorContext(ctx, "Response comparison setup failed", "error", err)
		return
	}
	h.health.SetReady(true)
}

func (h *Handler) loadDescriptors(ctx context.Context) (*descriptorpb.FileDescriptorSet, *descriptorpb.FileDescriptorSet, error) {
	type descriptorResult struct {
		name string
		set  *descriptorpb.FileDescriptorSet
		err  error
	}
	results := make(chan descriptorResult, 2)
	// Both loads share one deadline and each goroutine publishes exactly one result.
	for name, endpoint := range map[string]string{"reference": h.config.Reference, "candidate": h.config.Candidate} {
		go func() {
			set, err := h.descriptors.Load(ctx, endpoint)
			results <- descriptorResult{name: name, set: set, err: err}
		}()
	}
	sets := map[string]*descriptorpb.FileDescriptorSet{}
	for range 2 {
		loaded := <-results
		if loaded.err != nil {
			return nil, nil, errors.Wrapf(loaded.err, "load %s descriptors", loaded.name)
		}
		if loaded.set == nil {
			return nil, nil, errors.Errorf("load %s descriptors: descriptor set is nil", loaded.name)
		}
		sets[loaded.name] = loaded.set
	}
	return sets["reference"], sets["candidate"], nil
}

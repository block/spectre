package ingress

import (
	"context"
	"net"
	"net/http"

	"github.com/alecthomas/errors"
	"golang.org/x/net/netutil"
)

// Serve accepts ingress traffic until the context is cancelled or the server fails.
func (h *Handler) Serve(ctx context.Context, listener net.Listener) error {
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
	go func() { serveDone <- server.Serve(limitedListener) }()
	h.log.InfoContext(ctx, "Ingress proxy listening", "address", listener.Addr().String())

	select {
	case err := <-serveDone:
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

package proxy

import (
	"context"
	"log/slog"
	"net"
	"net/http"

	"github.com/alecthomas/errors"
	"golang.org/x/net/netutil"
)

// Binding pairs a listener with the handler that serves its connections.
type Binding struct {
	listener net.Listener
	handler  http.Handler
}

// NewBinding serves connections accepted by listener with handler.
func NewBinding(listener net.Listener, handler http.Handler) Binding {
	return Binding{listener: listener, handler: handler}
}

func (b Binding) address() string {
	return b.listener.Addr().String()
}

// start serves the binding in the background and sends its result to done.
func (b Binding) start(ctx context.Context, config Config, done chan<- error) *http.Server {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Handler:           b.handler,
		Protocols:         protocols,
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		IdleTimeout:       config.IdleTimeout,
		MaxHeaderBytes:    config.MaxHeaderBytes,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	limitedListener := netutil.LimitListener(b.listener, config.MaxConnections)
	go func() { done <- server.Serve(limitedListener) }()
	return server
}

// Lifecycle holds the proxy-specific steps that Serve runs around its servers.
type Lifecycle struct {
	onStart func(ctx context.Context)
	onStop  func()
	onDrain func(ctx context.Context) error
}

// NewLifecycle runs start once every listener accepts connections, stop before
// servers shut down, and drain after they have stopped.
func NewLifecycle(start func(ctx context.Context), stop func(), drain func(ctx context.Context) error) Lifecycle {
	return Lifecycle{onStart: start, onStop: stop, onDrain: drain}
}

func (l Lifecycle) start(ctx context.Context) {
	l.onStart(ctx)
}

func (l Lifecycle) stop() {
	l.onStop()
}

func (l Lifecycle) drain(ctx context.Context) error {
	return l.onDrain(ctx)
}

// Serve runs every binding until the context is cancelled or any server fails,
// then shuts all of them down before draining work that outlives requests.
func Serve(ctx context.Context, config Config, log *slog.Logger, bindings []Binding, lifecycle Lifecycle) error {
	// Explicit shutdown owns request lifetime after the signal starts shutdown ordering.
	serverContext := context.WithoutCancel(ctx)
	serveDone := make(chan error, len(bindings))
	servers := make([]*http.Server, 0, len(bindings))
	for _, binding := range bindings {
		servers = append(servers, binding.start(serverContext, config, serveDone))
		log.InfoContext(ctx, "Proxy listening", "address", binding.address())
	}
	lifecycle.start(ctx)

	shutdownErrors := make([]error, 0, 2*len(servers)+2)
	running := len(servers)
	select {
	case err := <-serveDone:
		running--
		shutdownErrors = append(shutdownErrors, serveError(err))
	case <-ctx.Done():
	}
	lifecycle.stop()
	shutdownContext, cancel := context.WithTimeout(serverContext, config.ShutdownTimeout)
	defer cancel()
	// Stop inbound handlers first so candidate admission cannot race with shutdown.
	for _, server := range servers {
		if err := server.Shutdown(shutdownContext); err != nil {
			shutdownErrors = append(shutdownErrors, errors.Wrap(err, "shut down HTTP server"))
			if closeErr := server.Close(); closeErr != nil {
				shutdownErrors = append(shutdownErrors, errors.Wrap(closeErr, "close HTTP server"))
			}
		}
	}
	if err := lifecycle.drain(shutdownContext); err != nil {
		shutdownErrors = append(shutdownErrors, err)
	}
	for range running {
		shutdownErrors = append(shutdownErrors, serveError(<-serveDone))
	}
	return errors.Join(shutdownErrors...)
}

func serveError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return errors.Wrap(err, "serve HTTP")
}

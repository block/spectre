package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/ingress"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/schema"
)

type cli struct {
	Log        logger.Config     `embed:""`
	Version    kong.VersionFlag  `help:"Print the version and exit."`
	Ingress    ingress.Config    `embed:""`
	Comparison comparison.Config `embed:""`
}

type commandContext struct {
	ctx context.Context
	log *slog.Logger
}

func main() {
	command := &cli{}
	kctx := kong.Parse(command, kong.Vars{"version": internal.Version})
	log := logger.New(command.Log, os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithLogger(ctx, log)
	kctx.FatalIfErrorf(kctx.Run(&commandContext{ctx: ctx, log: log}))
}

func (c *cli) Run(runtime *commandContext) error {
	transport := ingress.NewTransport(c.Ingress)
	defer transport.CloseIdleConnections()
	comparator, err := comparison.New(runtime.ctx, c.Comparison, runtime.log)
	if err != nil {
		return errors.Wrap(err, "configure response comparison")
	}
	handler, err := ingress.New(c.Ingress, transport, schema.NewReflectionLoader(), comparator, runtime.log)
	if err != nil {
		return errors.Wrap(err, "configure ingress")
	}
	network, address := c.Ingress.ListenNetworkAddress()
	if network == "unix" {
		if err := removeStaleSocket(address); err != nil {
			return errors.Wrap(err, "prepare ingress socket")
		}
	}
	listener, err := (&net.ListenConfig{}).Listen(runtime.ctx, network, address)
	if err != nil {
		return errors.Wrap(err, "listen for HTTP requests")
	}
	return errors.Wrap(handler.Serve(runtime.ctx, listener), "run ingress")
}

// removeStaleSocket clears a leftover path socket so a restart can bind it.
// Abstract sockets begin with "@" and need no filesystem cleanup.
func removeStaleSocket(address string) error {
	if strings.HasPrefix(address, "@") {
		return nil
	}
	info, err := os.Lstat(address)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "inspect socket path")
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.Errorf("refusing to remove non-socket file %q", address)
	}
	return errors.Wrap(os.Remove(address), "remove stale socket")
}

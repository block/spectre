package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/comparison"
	"github.com/block/spectre/internal/egress"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/proxy"
)

type cli struct {
	Log        logger.Config     `embed:""`
	Version    kong.VersionFlag  `help:"Print the version and exit."`
	Egress     egress.Config     `embed:""`
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
	transport := proxy.NewTransport(c.Egress.Config)
	defer transport.CloseIdleConnections()
	hasher, err := comparison.NewRequestHasher(runtime.ctx, c.Comparison, runtime.log)
	if err != nil {
		return errors.Wrap(err, "configure request hashing")
	}
	handler, err := egress.New(c.Egress, transport, hasher, runtime.log)
	if err != nil {
		return errors.Wrap(err, "configure egress")
	}
	addresses := []string{c.Egress.ReferenceListen, c.Egress.CandidateListen, c.Egress.HealthListen}
	listeners := make([]net.Listener, 0, len(addresses))
	for _, address := range addresses {
		listener, err := netaddr.ParseListen(address).Listen(runtime.ctx)
		if err != nil {
			return errors.Join(errors.Wrapf(err, "listen on %s", address), closeListeners(listeners))
		}
		listeners = append(listeners, listener)
	}
	return errors.Wrap(handler.Serve(runtime.ctx, listeners[0], listeners[1], listeners[2]), "run egress")
}

func closeListeners(listeners []net.Listener) error {
	closeErrors := make([]error, 0, len(listeners))
	for _, listener := range listeners {
		closeErrors = append(closeErrors, listener.Close())
	}
	return errors.Join(closeErrors...)
}

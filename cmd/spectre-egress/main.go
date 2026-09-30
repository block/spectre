package main

import (
	"context"
	"log/slog"
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
	reference, err := netaddr.ParseListen(c.Egress.ReferenceListen).Listen(runtime.ctx)
	if err != nil {
		return errors.Wrap(err, "listen for reference requests")
	}
	candidate, err := netaddr.ParseListen(c.Egress.CandidateListen).Listen(runtime.ctx)
	if err != nil {
		return errors.Join(errors.Wrap(err, "listen for candidate requests"), reference.Close())
	}
	return errors.Wrap(handler.Serve(runtime.ctx, reference, candidate), "run egress")
}

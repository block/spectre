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
	"github.com/block/spectre/internal/ingress"
	"github.com/block/spectre/internal/logger"
)

type cli struct {
	Log            logger.Config    `embed:""`
	Version        kong.VersionFlag `help:"Print the version and exit."`
	ingress.Config `embed:""`
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
	transport := ingress.NewTransport(c.Config)
	defer transport.CloseIdleConnections()
	handler, err := ingress.New(c.Config, transport, runtime.log)
	if err != nil {
		return errors.Wrap(err, "configure ingress")
	}
	listener, err := (&net.ListenConfig{}).Listen(runtime.ctx, "tcp", c.Listen)
	if err != nil {
		return errors.Wrap(err, "listen for HTTP requests")
	}
	return errors.Wrap(handler.Serve(runtime.ctx, listener), "run ingress")
}

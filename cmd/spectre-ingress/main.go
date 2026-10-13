package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
	kongtoml "github.com/alecthomas/kong-toml"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/httpserver"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/netaddr"
	"github.com/block/spectre/internal/protocol"
)

type cli struct {
	Log     logger.Config            `embed:""`
	Config  kong.ConfigFlag          `placeholder:"FILE" help:"Load flag values from a TOML file. Command-line flags take precedence."`
	Version kong.VersionFlag         `help:"Print the version and exit."`
	Ingress httpserver.IngressConfig `embed:""`
	Engine  protocol.Config          `embed:""`
}

type commandContext struct {
	ctx context.Context
	log *slog.Logger
}

func main() {
	command := &cli{}
	kctx := kong.Parse(command, kong.Vars{"version": internal.Version}, kong.Configuration(kongtoml.Loader))
	log := logger.Tagged(logger.New(command.Log, os.Stderr))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithLogger(ctx, log)
	kctx.FatalIfErrorf(kctx.Run(&commandContext{ctx: ctx, log: log}))
}

func (c *cli) Run(runtime *commandContext) error {
	transport := httpserver.NewTransport(c.Ingress.Config)
	defer transport.CloseIdleConnections()
	loader := Some[httpserver.DescriptorLoader](descriptors.NewReflectionLoader())
	handler, err := httpserver.NewIngress(runtime.ctx, c.Ingress, c.Engine, transport, loader, runtime.log)
	if err != nil {
		return errors.Wrap(err, "configure ingress")
	}
	listener, err := netaddr.ParseListen(c.Ingress.Listen).Listen(runtime.ctx)
	if err != nil {
		return errors.Wrap(err, "listen for HTTP requests")
	}
	return errors.Wrap(handler.Serve(runtime.ctx, listener), "run ingress")
}

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
	"github.com/block/spectre/internal/schema"
)

type cli struct {
	Log         logger.Config    `embed:""`
	Version     kong.VersionFlag `help:"Print the version and exit."`
	CheckSchema checkSchemaCmd   `cmd:"" help:"Validate a local protobuf descriptor set."`
	Serve       serveCmd         `cmd:"" help:"Mirror HTTP requests to reference and candidate backends."`
}

type checkSchemaCmd struct {
	DescriptorSet string `arg:"" type:"existingfile" help:"Binary protobuf descriptor set, including imports."`
}

type serveCmd struct {
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

func (c *checkSchemaCmd) Run(runtime *commandContext) error {
	data, err := os.ReadFile(c.DescriptorSet)
	if err != nil {
		return errors.Wrap(err, "read descriptor set")
	}
	if _, err := schema.New(data); err != nil {
		return errors.Wrap(err, "validate descriptor set")
	}
	runtime.log.InfoContext(runtime.ctx, "Descriptor set is valid", "path", c.DescriptorSet)
	return nil
}

func (c *serveCmd) Run(runtime *commandContext) error {
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

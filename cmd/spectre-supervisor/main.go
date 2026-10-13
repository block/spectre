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

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/supervisor"
)

type cli struct {
	Log        logger.Config     `embed:""`
	Config     kong.ConfigFlag   `placeholder:"FILE" help:"Load flag values from a TOML file. Command-line flags take precedence."`
	Version    kong.VersionFlag  `help:"Print the version and exit."`
	Supervisor supervisor.Config `embed:""`
}

type commandContext struct {
	ctx    context.Context
	log    *slog.Logger
	logCfg logger.Config
}

func main() {
	// A re-exec through the egress sandbox installs the seccomp filter then execs
	// the real command, so it must run before any flag parsing.
	if len(os.Args) > 1 && os.Args[1] == supervisor.SandboxArg {
		if err := supervisor.ExecSandbox(os.Args[2:]); err != nil {
			logger.New(logger.NewConfig(), os.Stderr).Error("Egress sandbox failed", "error", err)
			os.Exit(1)
		}
		return
	}

	command := &cli{}
	kctx := kong.Parse(command, kong.Vars{"version": internal.Version}, kong.Configuration(kongtoml.Loader))
	log := logger.Tagged(logger.New(command.Log, os.Stderr))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithLogger(ctx, log)
	kctx.FatalIfErrorf(kctx.Run(&commandContext{ctx: ctx, log: log, logCfg: command.Log}))
}

func (c *cli) Run(runtime *commandContext) error {
	super, err := supervisor.New(c.Supervisor, runtime.logCfg, runtime.log, os.Stdout)
	if err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(super.Run(runtime.ctx))
}

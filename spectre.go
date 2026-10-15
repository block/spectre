// Package spectre is the public API: a custom binary calls Main with options
// that add extensions, and inherits every built-in command.
package spectre

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

// Command is a top-level command. Kong parses its tagged fields before Run.
type Command interface {
	Run(ctx context.Context, log *slog.Logger) error
}

// Option registers an extension with Main.
type Option func(registered *extensions)

// extensions collects what the options register, before the parser is built.
type extensions struct {
	commands []kong.Option
}

// WithCommand adds a top-level command. command must be a pointer to a struct,
// because Kong parses flags into its fields.
func WithCommand(name string, help string, command Command) Option {
	return func(registered *extensions) {
		registered.commands = append(registered.commands, kong.DynamicCommand(name, help, "", command))
	}
}

// Main runs the command line with the given extensions, then exits.
func Main(options ...Option) {
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
	parser, err := newParser(command, options...)
	if err != nil {
		logger.New(logger.NewConfig(), os.Stderr).Error("Invalid command line", "error", err)
		os.Exit(1)
	}
	kctx, err := parser.Parse(os.Args[1:])
	parser.FatalIfErrorf(err)
	log := logger.Tagged(logger.New(command.Log, os.Stderr))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithLogger(ctx, log)
	kctx.BindTo(ctx, (*context.Context)(nil))
	kctx.Bind(log)
	kctx.FatalIfErrorf(kctx.Run(command.Log))
}

// newParser builds the parser for command with every registered extension.
func newParser(command *cli, options ...Option) (*kong.Kong, error) {
	registered := &extensions{}
	for _, option := range options {
		option(registered)
	}
	kongOptions := append([]kong.Option{kong.Vars{"version": internal.Version}, kong.Configuration(kongtoml.Loader)}, registered.commands...)
	parser, err := kong.New(command, kongOptions...)
	if err != nil {
		return nil, errors.Wrap(err, "build command line")
	}
	return parser, nil
}

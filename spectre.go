// Package spectre is the public API: a custom binary calls Main with options
// that add extensions, and inherits every built-in command.
package spectre

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alecthomas/errors"
	"github.com/alecthomas/kong"
	kongtoml "github.com/alecthomas/kong-toml"

	"github.com/block/spectre/internal"
	"github.com/block/spectre/internal/cli"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/protocols"
	httpprotocol "github.com/block/spectre/internal/protocols/http"
	mysqlprotocol "github.com/block/spectre/internal/protocols/mysql"
	statsdprotocol "github.com/block/spectre/internal/protocols/statsd"
)

// Command is a top-level command. Kong parses its tagged fields before Run.
type Command interface {
	Run(ctx context.Context, log *slog.Logger) error
}

// collectorServersVar names the Kong variable holding the default collector servers.
const collectorServersVar = "collector_servers"

// Option registers an extension with Main.
type Option func(registered *extensions)

// extensions collects what the options register, before the parser is built.
type extensions struct {
	kongOptions []kong.Option
}

// WithCommand adds a top-level command. command must be a pointer to a struct,
// because Kong parses flags into its fields.
func WithCommand(name string, help string, command Command) Option {
	return func(registered *extensions) {
		registered.kongOptions = append(registered.kongOptions, kong.DynamicCommand(name, help, "", command))
	}
}

// WithTemplateRepo sets the default repository spectre init fetches templates
// from: a git source such as git+ssh://github.com/org/repo, or a local directory.
func WithTemplateRepo(source string) Option {
	return func(registered *extensions) {
		registered.kongOptions = append(registered.kongOptions, kong.Vars{"template_repo": source})
	}
}

// WithServerURL sets the default base URL of the SPECTRE server that commands
// such as spectre status and spectre proxy call.
func WithServerURL(url string) Option {
	return func(registered *extensions) {
		registered.kongOptions = append(registered.kongOptions, kong.Vars{"server_url": url, collectorServersVar: url})
	}
}

// WithCollectorServers sets the collector servers that spectre status lists and
// clears by default. Pass it after WithServerURL, which also sets them.
func WithCollectorServers(urls ...string) Option {
	return func(registered *extensions) {
		registered.kongOptions = append(registered.kongOptions, kong.Vars{collectorServersVar: strings.Join(urls, ",")})
	}
}

// WithKongOptions passes options to the parser after the built-in ones, so
// kong.Vars adds to the built-in variables, such as version.
func WithKongOptions(options ...kong.Option) Option {
	return func(registered *extensions) {
		registered.kongOptions = append(registered.kongOptions, options...)
	}
}

// builtinRegistry registers the protocols that SPECTRE proxies.
func builtinRegistry() (*protocols.Registry, error) {
	registry := protocols.NewRegistry()
	err := errors.Join(
		registry.Register(httpprotocol.NewProvider()),
		registry.Register(mysqlprotocol.NewProvider()),
		registry.Register(statsdprotocol.NewProvider()),
	)
	return registry, errors.Wrap(err, "register protocols")
}

// Main runs the command line with the given extensions, then exits.
func Main(options ...Option) {
	registry, err := builtinRegistry()
	if err != nil {
		logger.New(logger.NewConfig(), os.Stderr).Error("Invalid protocols", "error", err)
		os.Exit(1)
	}
	command := cli.NewCommandLine(registry.Plugins())
	parser, err := newParser(command, options...)
	if err != nil {
		logger.New(logger.NewConfig(), os.Stderr).Error("Invalid command line", "error", err)
		os.Exit(1)
	}
	kctx, err := parser.Parse(os.Args[1:])
	parser.FatalIfErrorf(err)
	log := logger.New(command.Log, os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithLogger(ctx, log)
	kctx.BindTo(ctx, (*context.Context)(nil))
	kctx.Bind(log, cli.NewOutput(os.Stdout), registry, registry.Kinds(), registry.Kinds().NodeTypes(), registry.Bindings())
	kctx.FatalIfErrorf(kctx.Run())
}

// newParser builds the parser for command with every registered extension.
func newParser(command *cli.CommandLine, options ...Option) (*kong.Kong, error) {
	registered := &extensions{}
	for _, option := range options {
		option(registered)
	}
	kongOptions := append([]kong.Option{
		kong.Vars{"version": internal.Version, "template_repo": "", "server_url": "", collectorServersVar: ""},
		kong.Configuration(kongtoml.Loader),
		kong.DefaultEnvars("SPECTRE"),
	}, registered.kongOptions...)
	parser, err := kong.New(command, kongOptions...)
	if err != nil {
		return nil, errors.Wrap(err, "build command line")
	}
	return parser, nil
}

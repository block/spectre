// Command spectre runs the supervisor with its embedded proxies and generates
// the TypeScript schema declarations used for comparison.
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
	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/descriptors"
	"github.com/block/spectre/internal/jsonschema"
	"github.com/block/spectre/internal/logger"
	"github.com/block/spectre/internal/supervisor"
)

type cli struct {
	Log     logger.Config    `embed:""`
	Config  kong.ConfigFlag  `placeholder:"FILE" help:"Load flag values from a TOML file. Command-line flags take precedence."`
	Version kong.VersionFlag `help:"Print the version and exit."`

	Run runCommand `cmd:"" help:"Run the supervisor with the embedded proxies, splitting resources between a candidate and a reference."`
	Gen genCommand `cmd:"" help:"Generate TypeScript schema declarations and the script API."`
}

// runCommand supervises the candidate and reference behind the embedded proxies.
type runCommand struct {
	Supervisor supervisor.Config `embed:""`
}

// genCommand groups the schema declaration generators.
type genCommand struct {
	Proto      protoCommand      `cmd:"" help:"Generate TypeScript schema declarations from protobuf descriptor sets."`
	JSONSchema jsonSchemaCommand `cmd:"" name:"json-schema" help:"Generate a TypeScript schema declaration from JSON Schema files."`
	Module     moduleCommand     `cmd:"" help:"Write the spectre script API declaration for editors."`
}

type protoCommand struct {
	Descriptors descriptors.Config `embed:""`
	Output      string             `required:"" type:"path" placeholder:"DIR" help:"Directory to write one .d.ts file per protobuf file into."`
}

type jsonSchemaCommand struct {
	JSONSchema jsonschema.Config `embed:""`
	Output     string            `required:"" type:"path" placeholder:"FILE" help:"Path to write the module's .d.ts file to."`
}

type moduleCommand struct {
	Output string `required:"" type:"path" placeholder:"FILE" help:"Path to write the spectre.d.ts module declaration to."`
}

// commandContext carries the runtime wiring shared by the commands.
type commandContext struct {
	ctx context.Context
	log *slog.Logger
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
	kctx.BindTo(ctx, (*context.Context)(nil))
	kctx.FatalIfErrorf(kctx.Run(&commandContext{ctx: ctx, log: log}, command.Log))
}

func (c *runCommand) Run(runtime *commandContext, logConfig logger.Config) error {
	super, err := supervisor.New(c.Supervisor, logConfig, runtime.log, os.Stdout)
	if err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(super.Run(runtime.ctx))
}

func (c *protoCommand) Run(ctx context.Context) error {
	if c.Descriptors.DescriptorsDir == "" {
		return errors.New("a descriptors directory is required")
	}
	set, err := descriptors.LoadDescriptorSets(c.Descriptors)
	if err != nil {
		return errors.WithStack(err)
	}
	return errors.Wrap(descriptors.WriteDeclarations(ctx, set, c.Output), "write declarations")
}

func (c *jsonSchemaCommand) Run(ctx context.Context) error {
	return errors.Wrap(jsonschema.WriteDeclarations(ctx, c.JSONSchema, c.Output), "write declarations")
}

func (c *moduleCommand) Run() error {
	return errors.Wrap(javascript.WriteModuleDeclaration(c.Output), "write spectre module")
}
